# Phoenix — Architecture & Interview Defense Guide

A complete walkthrough of the Phoenix multiplayer backend platform: what it is,
how it's built, every design decision and its rationale, the trade-offs, what's
done vs. not, and a Q&A bank for interviews. Everything here reflects code that
actually runs and has been verified end-to-end.

---

## 1. One-paragraph pitch

> Phoenix is a reusable, event-sourced **multiplayer backend platform** written in
> Go. A game developer imports the SDK, writes only their game rules as a reducer,
> and calls `Run()` — Phoenix supplies authentication, rooms, WebSocket transport,
> an append-only event log, server-authoritative state, replay, presence,
> matchmaking, leaderboards, and an admin dashboard. The same backend powers any
> game; the flagship demo is online chess with a full rules engine, played in the
> browser, with a live admin dashboard that streams every event and can replay any
> match on a timeline. The architecture is **event-driven**: a synchronous,
> authoritative write path (validate → append → reduce → broadcast) plus
> asynchronous read-side projections (presence, leaderboard, dashboard) fed by an
> internal **event bus**.

**30-second version:** "It's like Firebase/PlayFab but event-sourced. You write a
reducer, you get a multiplayer backend. Everything that happens is an immutable
event in an append-only log, so state is a pure fold over that log — which gives
you replay, reconnection, audit, and rebuildable read models for free."

---

## 2. The core idea: everything is an event

Phoenix never stores mutable state directly. Instead of `player.position = (20,40)`
it stores a `PlayerMoved` event. Instead of `health = 30` it stores `DamageTaken`.

```
Client intent ──▶ Gateway ──▶ validate ──▶ append event ──▶ reduce ──▶ new state ──▶ broadcast
                                (rules)     (append-only log)  (pure fn)              (to room)
```

Three properties fall out of this design, and they are the heart of every
interview answer:

1. **Replay / time-travel** — the full match is the ordered list of its events, so
   you can reconstruct the state at any point by re-folding events up to that point.
2. **Server authority** — clients send *intents*, not state. The server validates
   each intent against the rules before it ever becomes an event, so the log only
   ever contains legal history. Clients can't cheat by mutating state.
3. **Crash recovery / reconnection** — because state is a pure function of the log,
   a crashed room (or a reconnecting player) is rebuilt by replaying its events.

This is **event sourcing**. The event log is the single source of truth; all other
state (current game state, leaderboards, presence) is *derived* from it.

---

## 3. High-level architecture

```
                         Browser clients (Next.js)
                    /play (chess)        /admin (dashboard)
                          │                    │
                   WebSocket + REST      REST + WS stream
                          │                    │
   ┌──────────────────────────────────────────────────────────────┐
   │                      Phoenix backend (Go)                      │
   │                                                                │
   │  Auth ──▶ Gateway(WS) ──▶ State Hub ──▶ Event Store(Postgres)  │  ← write path (sync)
   │            (heartbeat,     (reducer,        │                  │
   │             reconnect)      validate,       │ publish          │
   │                             broadcast)      ▼                  │
   │                                        Event Bus               │
   │                                       (pub/sub)                │
   │                          ┌──────────────┼───────────────┐      │  ← read side (async)
   │                       Presence      Leaderboard      Dashboard │
   │                       (Redis)       (Postgres        live      │
   │                                      read model)     stream    │
   └──────────────────────────────────────────────────────────────┘
                          │                    │
                       Postgres              Redis
              (users, rooms, events,      (presence)
               leaderboard projection)
```

**The single most important boundary:** the **write path is synchronous and
authoritative**; the **read side is asynchronous and eventually consistent**. A
game needs ordered, low-latency, validated delivery to the players in a room.
Presence, leaderboards, and dashboards are derived concerns that must never slow a
game down — so they live behind an event bus and update in the background.

---

## 4. Component-by-component

The repo is a single Go module (`github.com/fazal/phoenix`) plus a Next.js app.

### 4.1 `internal/core` — the shared domain (leaf package)
Holds the types and interfaces every subsystem depends on, so nothing has import
cycles. Key types:
- `Event{ID, Seq, Type, RoomID, PlayerID, Payload, Timestamp, Version}` — the
  atomic unit. `Seq` is a **per-room monotonic** sequence (ordering within a room);
  `Payload` is opaque game-defined JSON.
- `Reducer func(state, Event) (newState, error)` — folds an event into state.
- `EventStore{ Append, Load }` — the durable write side.
- `Publisher{ Publish(Event) }` — the bus seam the store publishes to.
- `DerivedEvent` + `Deriver` — server-authored domain events from transitions.
- `PresenceStore`, `Matchmaker` — pluggable interfaces.

The public SDK package `pkg/phoenix` re-exports these as **type aliases**
(`type Event = core.Event`), so a developer imports one package while internals
share the exact same types.

### 4.2 `internal/store` — Event store (the system of record)
- **`Postgres`** — append-only `events` table. `Append` runs in a transaction,
  takes a **per-room advisory lock** (`pg_advisory_xact_lock(hashtext(room_id))`),
  computes the next `Seq`, inserts, commits, then **publishes to the bus**. Different
  rooms append fully in parallel; the same room is serialized so `Seq` has no gaps.
- **`Memory`** — same interface, in-memory, for tests/demos. Its existence is the
  proof the engine is decoupled from storage.
- **`UNIQUE(room_id, seq)`** in the schema guarantees per-room total ordering at the
  database level.

### 4.3 `internal/eventbus` — the event bus
- `Bus{ Publish, Subscribe }`; in-process implementation `InProcess`.
- Each subscriber gets its **own buffered channel drained by its own goroutine**, so
  a slow consumer never blocks the publisher or other consumers.
- `Publish` is **non-blocking**: if a subscriber's queue is full, the event is
  dropped and counted (lag), never blocking the authoritative write path.
- Subscriptions can be **filtered by room** (`WithRoom`) — the dashboard watches one
  match without seeing the firehose.
- Satisfies `core.Publisher`, so the store publishes to it without knowing it's a bus.

### 4.4 `internal/state` — the State Hub (the heart)
- One `Hub` per game; one in-memory `game` runtime per active room (members +
  current state), guarded by a mutex so a room's events apply in one serialized order.
- `dispatch()` is the core loop, holding the room lock across the whole sequence so
  **log order always equals state-mutation order**:
  1. run the reducer on prior state (validate);
  2. if it errors and this is a client intent → **reject** (nothing persisted);
  3. else `store.Append` (durable, assigns `Seq`, publishes to bus);
  4. set new state, broadcast the delta to the room's sockets;
  5. compute **derived events** from the transition and emit them through the same
     pipeline (recursion-guarded so derived events don't re-derive).
- `getOrCreate` rebuilds a room's state by replaying its event log — this is
  reconnection and crash recovery.

### 4.5 `internal/gateway` — WebSocket gateway
- Upgrades HTTP → WS (`coder/websocket`), authenticates the handshake (JWT via
  `?token=`), validates the room, checks bans.
- One **writer goroutine per connection** serializes all socket writes (the WS lib
  forbids concurrent writes); `Send` only enqueues, dropping on overflow rather than
  stalling the hub.
- **Heartbeat:** periodic ping; a failed ping/write cancels the connection.
- **Reconnect grace window:** when a socket drops, the player is detached but stays
  *logically present* for `ReconnectWindow` (30s). Reconnecting within the window
  cancels the pending `PlayerLeft` and re-attaches with a fresh snapshot — no
  duplicate join event. Only after the window elapses does `PlayerLeft` fire.

### 4.6 `internal/auth` — identity
- Guest login and email login (passwordless for now); short-lived **JWT access
  tokens** (HS256, 15m) + long-lived **rotating refresh tokens** stored hashed
  (SHA-256). Refresh is single-use: using one deletes it and issues a new pair, so a
  leaked refresh token is usable at most once.
- `Middleware` (required) and `Optional` (enrich-if-present) for HTTP; `Authenticate`
  for the WS handshake.

### 4.7 `internal/room` + `internal/matchmaking`
- Room CRUD (create/list/get/terminate) in Postgres; private rooms get invite codes.
- Matchmaking v1 = **quick-match**: first caller creates a room and waits, second is
  paired in. Implements `core.Matchmaker`, so ranked/skill matching slots in behind
  the same interface.

### 4.8 Read-side consumers (the event-driven payoff)
- **`internal/presence` (Redis)** — subscribes to the bus; on `PlayerJoined` marks
  the player online/playing (Redis `SET` + `SADD presence:online`), on `PlayerLeft`
  offline (`SREM`). `OnlineCount` = `SCARD`. Shared across nodes; ephemeral.
- **`internal/leaderboard` (Postgres read model / CQRS projection)** — subscribes to
  the bus; on `MatchEnded` it **recomputes standings by folding every `MatchEnded`
  event in the log**. Rebuilt from scratch on boot, so it's always exactly the fold
  of history and can never drift. `GET /leaderboard` serves it.
- **`internal/admin` live stream** — a per-room bus subscriber bridged to a WebSocket;
  the dashboard sees every event as it happens.

### 4.9 `pkg/phoenix` — the SDK
The developer-facing surface. `New()`, `OnJoin`, `OnLeave`, `OnEvent(type, reducer)`,
`InitialState`, `Derive`, `Run()`. `Run()` wires the entire backend (config → pool →
bus → store → auth → rooms → matchmaking → hub → gateway → presence → leaderboard →
admin → HTTP server with graceful shutdown) against the registered rules.

### 4.10 `internal/chess` — the rules engine
A self-contained, **perft-validated** chess engine (standard library only): legal
move generation, castling, en passant, promotion, pins, check/checkmate/stalemate,
SAN with disambiguation, FEN round-trip. *Perft* validation means move generation was
checked against known node counts (start position to depth 4 = 197,281; the Kiwipete
position to depth 3) — the gold standard for chess engine correctness.

### 4.11 `cmd/` — entrypoints
- **`cmd/chess`** — the chess game on the SDK (~170 lines of rules). This is the
  flagship: import SDK, register a `move` reducer + a `Derive` for
  `MatchStarted`/`MatchEnded`, `Run()`.
- `cmd/phoenix` — generic reference server. `cmd/seed` — plays a full Scholar's-mate
  game over real WebSockets to populate demo data. `cmd/smoke` — raw-protocol E2E client.

---

## 5. Data model

**Postgres**
- `users(id, email, is_guest, display_name, banned, created_at)`
- `refresh_tokens(token_hash PK, user_id, expires_at)` — hashed, rotated
- `rooms(id, owner_id, game_type, status, max_players, is_private, invite_code, created_at)`
- `events(id, room_id, seq, type, player_id, payload JSONB, version, timestamp,
  UNIQUE(room_id, seq))` — **the append-only log; never UPDATE/DELETE**
- `leaderboard(player_id PK, wins, losses, draws)` — a *projection*, fully rebuildable

**Redis**
- `presence:online` (set of online player ids) · `presence:status:<id>` (status string)

Why this split: Postgres is the durable, queryable system of record (events + read
models that need joins/ordering). Redis holds ephemeral, high-churn presence shared
across nodes.

---

## 6. Key flows

### 6.1 A chess move (synchronous write path)
```
1. Browser drags a piece → optimistic local validation (chess.js, UX only)
2. WS frame: {type:"move", payload:{from:"e2", to:"e4"}}
3. Gateway → Hub.HandleMessage → dispatch (holds the room lock)
4. moveReducer: load FEN → chess engine validates the move
      illegal / out-of-turn / not your seat → return error
        → server sends {type:"error"}, NOTHING persisted, board reverts
      legal ↓
5. store.Append(event) → Postgres (assigns Seq) → publish to bus
6. fold event into new state (new FEN, turn, status, SAN history)
7. broadcast {type:"event", state} to both players + spectators
8. Derive: if status became checkmate → emit MatchEnded (server-authored)
      → step 5–7 again for MatchEnded → bus → leaderboard recomputes
```

### 6.2 Join & reconnect
```
WS connect (?room&token) → auth → Hub.Join(isReconnect)
  fresh join  → PlayerJoined event → OnJoin → seats assigned → snapshot to client
  reconnect   → re-attach, send snapshot, NO duplicate join event
socket drop  → detach silently → start 30s grace timer
  reconnect in window → cancel timer
  window elapses       → PlayerLeft event → presence offline
```

### 6.3 Event-driven read side (async)
```
store.Append → bus.Publish(event)
   ├─ presence consumer   → Redis (online/offline)
   ├─ leaderboard consumer → on MatchEnded, refold log → standings
   └─ dashboard stream     → push to admin WebSocket (room-filtered)
```
The game loop does not know these consumers exist.

---

## 7. Design decisions & rationale (the "why" — interviewers probe this)

| Decision | Why | Trade-off accepted |
|---|---|---|
| **Event sourcing** (append-only log, state = fold) | Free replay, audit, crash recovery, server authority; rebuildable read models | Log grows unbounded (needs snapshots later); every read of "current state" is a fold (mitigated by in-memory runtime + projections) |
| **Sync write path, async read side** | Games need ordered, low-latency, authoritative delivery; derived data is eventually consistent and must not block games | Read models lag slightly; "did my win count?" is eventually, not instantly, consistent |
| **In-process event bus now, Kafka later** | At one node, channels beat a broker — no network hop, no ops. The `Bus`/`Publisher` interface means Kafka drops in unchanged for multi-node | In-process delivery is at-least-once *while running*, not durable; mitigated because projections rebuild from the log |
| **Postgres event store (not Kafka) as system of record** | Replay needs random access (jump to timestamp/seq) — a queryable indexed table beats a sequential log; it's also durable and transactional | Lower write throughput than Kafka; fine for turn-based, revisit for high-frequency games |
| **Per-room advisory lock for `Seq`** | Guarantees gap-free per-room ordering without a global lock; different rooms stay parallel | A tiny serialization point per room; negligible for turn-based |
| **Plugin interfaces from day 1** (`EventStore`, `Matchmaker`, `PresenceStore`, `Bus`) | Swap implementations (memory↔Postgres, random↔ranked, in-process↔Kafka) without touching the core; demonstrates extensible API design | A little extra indirection |
| **Server-authoritative validation before append** | Anti-cheat; the log only ever holds legal history, which keeps replay correct | The reducer runs on the hot path (cheap for chess) |
| **Reconnect grace window** | Mobile/flaky networks shouldn't eject a player mid-match | Holds a seat briefly after disconnect |
| **Rebuildable projections** (leaderboard refolds the log) | Read models can never drift from the source of truth; idempotent by construction | Recompute-from-scratch is O(history); fine for rare match-end events, optimize with incremental updates later |
| **Model A SDK** (game compiled into the server) | "Import SDK, add rules, `docker build`, deploy" — one binary is a complete backend; keeps the server authoritative | Game and platform deploy together (a separate-process model is the large-scale alternative) |
| **Derived domain events** (`MatchEnded` via `Derive`) | Consumers react to meaningful domain events, not raw inputs; the game loop stays ignorant of downstream features | Slight indirection; lifecycle logic lives in the game's `Derive` |

---

## 8. Reliability, security, scaling

**Reliability:** heartbeat ping/pong; 30s reconnect window with session recovery;
dead connections removed; crash recovery by replaying the log; graceful shutdown on
SIGINT/SIGTERM; a down consumer (e.g. Redis) degrades a feature, never a game.

**Security:** JWT access + rotating single-use refresh tokens (hashed at rest);
server-authoritative state (clients send intents, never state); input validation in
the reducer; ban enforcement at the WS handshake. *Gaps:* no rate limiting yet, no
OAuth, passwordless email login.

**Scaling path (single node today → distributed):** stateless gateway scales
horizontally; room servers scale horizontally with **sticky routing by `room_id`**;
Redis already gives shared presence; the event store partitions by room; the
**in-process bus becomes Kafka/NATS** (partition by room for per-room ordering,
consumers become consumer groups with offsets) — all behind interfaces that already
exist, so consumers don't change. gRPC between services and K8s are the Phase-3 step.

---

## 9. What's built vs. what's not (be honest — it reads as maturity)

**Built and verified:** auth (guest/email/JWT/refresh), rooms, WS gateway
(heartbeat + reconnect), state engine (validate→append→reduce→broadcast), Postgres
event store, **event bus**, **presence (Redis)**, **leaderboard (rebuildable
projection)**, quick-match, replay (log + dashboard scrubber), admin dashboard
(metrics, event inspector, live stream, terminate, ban), the SDK, a perft-validated
chess engine, and a browser chess client.

**Not built (deliberate scope):** chat; rate limiting; event-log snapshots
(compaction); distribution (Kafka, gRPC, sticky routing, K8s); OAuth & password auth;
host migration; rich metrics (CPU/latency/packet-loss); cheat-detection hooks.
Threefold-repetition/50-move draws aren't enforced (only insufficient-material).

**Delivery-semantics caveat:** the in-process bus is at-least-once *while the process
runs*; it is not a durable queue. That's acceptable precisely because the event log
is the source of truth and projections rebuild from it — and the upgrade path
(Kafka) is already designed in.

---

## 10. Why this is an SDE-2 project

Concretely demonstrates: high-concurrency Go (goroutines, channels, per-connection
writers, mutex-guarded room runtimes); long-lived WebSocket lifecycle management;
**event sourcing and CQRS read models**; **event-driven, loosely-coupled service
design**; reliable state sync, replay, and reconnection; API **and** SDK design
(developer experience); caching/persistence/fault recovery; and an explicit,
defensible boundary between synchronous authoritative writes and asynchronous
eventually-consistent reads — with a credible monolith→distributed evolution path.

---

## 11. Interview Q&A bank

**Q: Walk me through the architecture.**
Event sourcing on the write side — every action is an immutable event in a per-room
append-only log, and current state is a pure fold over that log, so the server is
authoritative and matches are replayable. On the read side, CQRS projections
(presence, leaderboard, dashboard) are built asynchronously off an event bus. The
write path is synchronous and ordered for game correctness; the read side is
eventually consistent so it never slows a game.

**Q: Why event sourcing instead of just storing current state?**
Three payoffs: replay/time-travel (reconstruct any point by re-folding), server
authority + audit (the log is immutable legal history), and crash
recovery/reconnection (rebuild state from the log). It also makes read models
rebuildable — any projection can be recomputed from the log.

**Q: Why an event bus? Isn't that over-engineering for one node?**
It's about coupling, not nodes. Services react to events instead of calling each
other, so adding a feature is adding a subscriber, not editing the core. Concretely:
the chess game emits `MatchEnded` and the leaderboard updates itself — the game code
never references the leaderboard. And the `Bus` interface means the in-process
implementation swaps for Kafka with zero consumer changes when I need multi-node.

**Q: Sync or async — and why both?**
The game loop is synchronous: a move must be validated, ordered, and broadcast with
low latency, and clients can't proceed until the server confirms. Presence,
leaderboards, and dashboards are derived and eventually consistent, so they're async
behind the bus and must never block a game. Drawing that boundary explicitly is the
key decision.

**Q: What happens if a consumer is slow or down?**
Publish never blocks the writer — each consumer has a bounded queue and a dedicated
goroutine; on overflow events are dropped and counted. Games keep running. Redis can
be down and the platform degrades (no live presence), it doesn't fail.

**Q: Then don't you lose events / how do projections stay correct?**
The event log in Postgres is the source of truth; the bus is just live distribution.
Projections rebuild from the log — the leaderboard literally recomputes from
`MatchEnded` events on boot, so it's always exactly the fold of history and can't
drift. With Kafka, the same idea is consumer-group offsets + replay.

**Q: How do you guarantee ordering?**
Within a room, the event store assigns a monotonic `Seq` under a per-room advisory
lock, and `UNIQUE(room_id, seq)` enforces it at the DB. The hub holds the room lock
across validate→append→apply, so log order equals state-mutation order. Different
rooms are fully parallel. Cross-room global ordering isn't needed.

**Q: How is it server-authoritative / cheat-resistant?**
Clients send intents, never state. The reducer validates each intent against the
rules *before* anything is appended; an illegal or out-of-turn move is rejected and
never enters the log. The board the client renders is just a view of the server's
authoritative state.

**Q: How does reconnection work?**
On disconnect the player is detached but stays logically present for a 30s grace
window. Reconnecting cancels the pending `PlayerLeft` and the server resends a state
snapshot (rebuilt from the log). Only if the window elapses does the player actually
leave.

**Q: How would you scale this to millions of players?**
Gateways are stateless → scale horizontally. Route players to room servers with
sticky routing by `room_id` so a room lives on one node. Redis already provides
shared presence. Swap the in-process bus for Kafka, partitioned by room for per-room
ordering; consumers become consumer groups with offsets. Partition the event store
by room. The interfaces for all of this already exist, so it's an implementation
swap, not a rewrite.

**Q: What's the SDK and how does a game use it?**
The SDK is the public Go package a game imports. The developer writes
`game.OnEvent("move", reducer)` plus `game.Derive(...)` for lifecycle events and
calls `game.Run()`, which boots the entire platform against those rules. One binary
is a complete, deployable multiplayer backend — `docker build` and ship.

**Q: What would you do next / what are the weaknesses?**
Event-log snapshots so replay doesn't refold from move 1; durable bus (Kafka) for
multi-node and exactly-once-ish processing with offsets; rate limiting; chat; and
real distribution (sticky routing, gRPC, K8s). The honest current limitation is the
in-process bus is at-least-once while running, not a durable queue — acceptable
because the log is the source of truth, and the upgrade path is designed in.

---

## 12. 60-second whiteboard script

"Clients talk WebSocket to a gateway. A move is an *intent* — the gateway hands it to
the room's state hub, which validates it against the game rules. If it's legal, it's
appended as an immutable event to a per-room append-only log in Postgres; that's the
source of truth. The hub folds the event into new state and broadcasts the delta to
everyone in the room — that whole path is synchronous and ordered because a game
needs authority and low latency. After the durable append, the event is published to
an internal event bus, and asynchronous consumers react: presence updates Redis, the
leaderboard refolds match results, the dashboard streams events live. Those are CQRS
read models — eventually consistent, rebuildable from the log, and they never block a
game. The whole thing is a reusable platform: a developer imports the SDK, writes a
reducer, and gets this entire backend. Today it's one node with an in-process bus;
the bus and store are interfaces, so going distributed is swapping in Kafka and
sticky room routing — not a rewrite."

---

## 13. Technical deep-dive Q&A (Go, architecture, infra)

The most-likely technical questions, grouped by topic. Answers are tight and tied
to this project where relevant. Study the **bold idea** in each — that's what an
interviewer is checking you understand.

### 13.1 Go — language & runtime

**Q: Why Go for this project?**
Goroutines + channels make tens of thousands of concurrent long-lived WebSocket
connections cheap and simple; static binaries make deployment trivial (`docker build`
→ one ~15MB binary); the standard library has a production HTTP server and (1.22+)
method+path routing; fast compile, GC, and strong tooling (`go vet`, race detector,
`pprof`). It's the language most multiplayer/infra backends (and the PRD) target.

**Q: Goroutine vs OS thread?**
A goroutine is a user-space green thread the Go runtime multiplexes onto a small pool
of OS threads (the M:N scheduler). It starts at ~2KB of stack (grows/shrinks
dynamically) vs ~1–8MB for an OS thread, so you can run hundreds of thousands. The
runtime scheduler handles blocking syscalls by parking the goroutine and reusing the
OS thread. **Cheap concurrency is why a goroutine-per-connection model is viable.**

**Q: Channels vs mutexes — when do you use which? Where in this project?**
Rule of thumb: **channels to transfer ownership of data / coordinate; mutexes to
protect shared mutable state.** In Phoenix: the per-connection **send queue is a
channel** (hand a message to the writer goroutine), and the **event bus uses a
buffered channel per subscriber** (hand events to a consumer). But the **room's
current state is a mutex** (`g.mu`) because many code paths read/modify the same
struct in place and need a critical section — modeling that as channels would be
awkward. "Don't communicate by sharing memory; share memory by communicating" is the
guideline, not a law.

**Q: What's `context.Context` for?**
Cancellation, deadlines, and request-scoped values across API boundaries. In Phoenix
each WebSocket connection has a `context.WithCancel`; a failed read/write/ping calls
`cancel()`, which unblocks the read and writer goroutines so the connection tears
down cleanly. HTTP handlers use `r.Context()`. **It's the standard way to propagate
"stop now" through a call tree of goroutines.**

**Q: How does Go's error handling work and why no exceptions?**
Errors are values returned explicitly (`if err != nil`). It forces you to handle
failures at each call site instead of unwinding through hidden catch blocks; wrapping
(`fmt.Errorf("...: %w", err)`) preserves a chain you can `errors.Is/As`. `panic`/
`recover` exist but are for truly exceptional/unrecoverable cases, not control flow.

**Q: What is an interface in Go and how is it different from Java?**
Interfaces are **satisfied structurally (implicitly)** — a type implements an
interface just by having the methods, no `implements` keyword. That's why Phoenix's
`Postgres` and `Memory` both satisfy `EventStore` without declaring it, and why I can
define an interface where it's *consumed*. An interface value is a (type, value) pair;
a nil interface is not the same as an interface holding a nil pointer (a classic Go
gotcha). I use compile-time assertions like `var _ core.EventStore = (*Postgres)(nil)`
to catch drift.

**Q: `defer` — what is it and any gotchas?**
`defer` schedules a call to run when the function returns (LIFO order); used for
unlocks, `Close`, `Rollback`. Gotchas: deferred args are evaluated immediately;
`defer` in a loop accumulates until function return (don't defer per-iteration in a
long loop); a deferred `tx.Rollback()` after `Commit()` is a safe no-op (the pattern
I use).

**Q: How does Go's garbage collector affect a low-latency game server?**
Go uses a concurrent, tri-color mark-sweep GC with sub-millisecond stop-the-world
pauses, tuned by `GOGC`. For turn-based chess it's irrelevant; for a high-frequency
real-time game you'd reduce allocations on the hot path (object pooling, avoid
per-event allocations, reuse buffers) and watch GC with `pprof`. **Know the tool
(`pprof`, `GODEBUG=gctrace=1`) and the mitigation (allocate less), not GC internals.**

**Q: How do you find a data race?**
Run with `go test -race` / `go run -race`. It instruments memory access and reports
concurrent unsynchronized access. The fix is a mutex or channel. The per-room mutex
and the per-connection writer goroutine in Phoenix exist specifically to avoid races
(e.g. the WS library forbids concurrent writes, so all writes funnel through one
goroutine).

### 13.2 Go concurrency — as used in Phoenix

**Q: You have a goroutine per connection. How do you avoid concurrent writes to one
socket?**
`coder/websocket` forbids concurrent writes. So each connection has **one writer
goroutine** draining a buffered `out` channel; everything that wants to send calls
`Send()` which just enqueues. Reads happen in a separate read loop. Pings are sent
from the writer goroutine's `select` so they never race with message writes.

**Q: Your `Send` drops messages if the buffer is full. Isn't that a bug?**
It's a deliberate **backpressure** choice: a slow client must not stall the hub (which
serves other players). If a client falls behind, we drop and it can resync from a
snapshot on reconnect — the server state is authoritative and recoverable. Blocking
the writer would create head-of-line blocking across the room.

**Q: Walk me through how you prevent state corruption when two moves arrive at once.**
The room runtime holds `g.mu` across the **entire** dispatch — validate → append →
apply → snapshot the connection list. So two intents for the same room are fully
serialized: log order equals state order. Different rooms have different locks and run
in parallel. The DB also enforces it via the per-room advisory lock + `UNIQUE(room_id,
seq)`.

**Q: You hold a mutex across a DB write (Append). Isn't that slow?**
Yes, it serializes that room's writes, and I accept it because (a) per-room throughput
for turn-based games is low, and (b) correctness (gap-free ordering, no lost updates)
matters more here. For a high-frequency game I'd move the append off the lock and use
an optimistic/seq-reservation scheme — a conscious trade-off, documented.

**Q: How does the event bus avoid a slow consumer blocking the publisher?**
Each subscriber has its **own buffered channel + goroutine**. `Publish` does a
non-blocking `select { case ch <- e: default: drop+count }`. So one slow consumer
can't block the publisher or other consumers. **This is the whole point of putting
derived work behind the bus.**

**Q: How do you cleanly shut down goroutines?**
Per-connection `context` cancel on error; the bus `Subscribe` returns an
`unsubscribe()` that closes a `done` channel the consumer goroutine selects on; the
HTTP server uses `srv.Shutdown(ctx)` on SIGINT/SIGTERM for graceful drain. **No
goroutine leaks: every long-lived goroutine has an explicit stop signal.**

### 13.3 Event sourcing & CQRS

**Q: Define event sourcing.**
Persist state as an ordered, immutable sequence of events; current state is a
left-fold (reduce) over those events. The log is the source of truth; you never
update-in-place. Contrast with CRUD, which stores only the latest state and loses
history.

**Q: Define CQRS and where you use it.**
Command Query Responsibility Segregation: separate the write model (commands that
produce events) from read models (queries served by purpose-built projections).
Phoenix: the write model is the event log + reducer (authoritative game state); read
models are the leaderboard (Postgres projection) and presence (Redis projection),
each shaped for its query and updated asynchronously off the bus.

**Q: Downsides of event sourcing?**
The log grows unbounded → you need **snapshots/compaction** (store periodic state so
replay starts from the latest snapshot, not event 1 — a known gap here). Reading
"current state" requires a fold (mitigated by keeping the live state in memory).
Schema/event evolution needs versioning (I keep a `version` field on every event).
Eventual consistency of read models confuses people who expect read-after-write.

**Q: How do you evolve an event's schema (versioning)?**
Each event carries a `version`. Reducers handle multiple versions (upcasting old
payloads on read), or you write a one-time migration that rewrites/snapshots. You
**never** retro-edit historical events — they're immutable facts.

**Q: How do you rebuild a projection, and why is that powerful?**
Replay the relevant events from the log and re-fold. The leaderboard literally does
this on boot (`RebuildFromLog` over all `MatchEnded` events). Power: a projection can
never permanently drift — if it's wrong, you rebuild it; if you want a *new* read
model later, you build it from history you already have. **The log is a time machine.**

**Q: What's a snapshot and when would you add one?**
A periodic materialization of folded state (e.g. every N events) so recovery/replay
starts from the snapshot and applies only the tail. Add it when replay-from-zero
becomes slow (long matches / large logs). It's an optimization, not a correctness
requirement — the events remain the source of truth.

### 13.4 Messaging & delivery semantics

**Q: At-least-once vs at-most-once vs exactly-once?**
At-most-once: deliver and maybe lose (no retry). At-least-once: retry until acked, so
**possible duplicates** → consumers must be idempotent. Exactly-once: no loss, no
dupes — expensive/often an illusion end-to-end; usually achieved as "at-least-once +
idempotent consumers" or transactional offset commits. Phoenix's in-process bus is
at-least-once *while running* (not durable); projections are made safe by being
idempotent (leaderboard recomputes from the log).

**Q: What does idempotent mean and how is your leaderboard idempotent?**
Applying the same operation twice yields the same result. The leaderboard recomputes
standings from scratch by folding all `MatchEnded` events, so processing the same
`MatchEnded` twice produces identical output — no double counting. (The incremental
"increment a counter" approach would *not* be idempotent without dedup by event id.)

**Q: How do you guarantee ordering in a message system?**
You generally only get ordering **within a partition/key**. Phoenix orders within a
room via the per-room `Seq`. In Kafka you'd **partition by `room_id`** so all of a
room's events land on one partition and stay ordered; global ordering across rooms
isn't needed and doesn't scale.

**Q: Pub/sub vs a message queue?**
Pub/sub fans one event out to *all* interested subscribers (presence AND leaderboard
AND dashboard all see it). A work queue distributes each message to *one* worker for
load-balancing. Phoenix needs fan-out, so pub/sub. Kafka consumer *groups* give you
both: fan-out across groups, load-balanced within a group.

**Q: What is backpressure and how do you handle it?**
When producers outpace consumers. Options: bounded buffers + drop (what the bus does
for derived consumers — never stall the game), block the producer (backpressure into
the caller — wrong here, it'd slow games), or spill to durable storage and let
consumers catch up via offsets (the Kafka answer). **Choosing drop-vs-block per
consumer is the design.**

### 13.5 WebSockets & realtime

**Q: Why WebSockets and not HTTP polling or SSE?**
A game needs low-latency **bidirectional** push. Polling is high-latency and wasteful;
SSE is server→client only (no client→server channel over the same connection).
WebSockets give a persistent full-duplex connection after one HTTP upgrade
handshake — clients send moves, server pushes deltas, both cheaply.

**Q: How does the WebSocket handshake work?**
Client sends an HTTP `GET` with `Upgrade: websocket` + `Sec-WebSocket-Key`; server
replies `101 Switching Protocols` with the accept hash; the TCP connection then
carries WS frames instead of HTTP. Phoenix authenticates *during* this handshake (JWT
in a query param, since browsers can't set headers on WS) and rejects before upgrading
if the token/room/ban check fails.

**Q: Why did cross-origin WebSockets break, and how did you fix it?**
`coder/websocket` checks the `Origin` header against the `Host` by default (anti-CSWSH).
The browser client on `:3100` connecting to the backend on `:8090` is cross-origin →
the handshake was rejected (Go clients with no `Origin` slipped through, which is why
tests passed but the browser didn't). Fix: allow the origin (`InsecureSkipVerify` for
dev; in prod use an explicit `OriginPatterns` allow-list). **Real lesson: same-origin
checks on WS upgrades.**

**Q: How do heartbeats/reconnection work and why?**
TCP can half-die without either side noticing. A periodic **ping/pong** detects dead
peers; a failed ping tears the connection down. On the client, an auto-reconnecting
socket re-opens and the server resends a snapshot. Phoenix adds a **30s grace window**
so a brief drop doesn't eject a player mid-match (the `PlayerLeft` event is deferred
and cancelled on reconnect).

**Q: How do you scale WebSockets horizontally? The "broadcast across nodes" problem.**
WS connections are sticky to a node. If two players in a room land on different nodes,
a broadcast on node A must reach node B. Solutions: **sticky routing by `room_id`** so
a whole room lives on one node (Phoenix's plan), and/or a shared pub/sub backplane
(Redis/Kafka) so nodes relay room events. Load-balance with a layer-4 LB or one that
supports WS upgrade; keep gateways stateless so any node can serve any connection.

### 13.6 PostgreSQL

**Q: Why Postgres as the event store instead of Kafka?**
Replay needs **random, queryable access** — "load room R, events 5..20" or "jump to
timestamp T" — which an indexed table does trivially and a sequential log does poorly.
Postgres is also durable, transactional, and lets read models do joins (leaderboard ⨝
users). I documented that the mature design is **both**: Postgres as system-of-record,
Kafka as the live transport when distributed.

**Q: What's the advisory lock doing and why not a normal row lock?**
`pg_advisory_xact_lock(hashtext(room_id))` serializes appends *for one room* so the
`SELECT MAX(seq)+1 ... INSERT` is atomic with no gaps/dupes, while different rooms run
in parallel. It's a lightweight, application-defined lock keyed on an arbitrary value —
cleaner than locking rows that may not exist yet (you're computing the *next* seq).
`UNIQUE(room_id, seq)` is the backstop.

**Q: Could you avoid the lock?**
Yes: a Postgres `SEQUENCE` or `BIGSERIAL` per room (awkward to create per room), or an
`INSERT ... SELECT COALESCE(MAX(seq),0)+1` with a retry on unique-violation
(optimistic concurrency), or push ordering to Kafka partitions. I chose the advisory
lock for simplicity and gap-free guarantees at low per-room write rates.

**Q: ACID? Which isolation level?**
Atomicity, Consistency, Isolation, Durability. Postgres default is Read Committed;
the advisory lock gives me the serialization I need for the append without going to
Serializable (which would add retry overhead). The append is wrapped in a transaction
so the lock + read + insert commit atomically.

**Q: Why JSONB for the payload? Trade-offs?**
The payload is game-defined and schema-flexible, so JSONB keeps the events table
generic across games. JSONB is queryable/indexable (GIN) if needed. Trade-off: less
schema enforcement than columns; for hot analytical queries you'd project specific
fields into typed columns or a read model.

**Q: This events table grows forever — what do you do?**
Partition by time or room (declarative partitioning), archive cold partitions to
cheap storage, and add **snapshots** so you rarely read old events. Index for the
access pattern (`(room_id, seq)` is the unique index; `(room_id, timestamp)` for
time-range replay).

### 13.7 Redis

**Q: Why Redis for presence specifically?**
Presence is **ephemeral, high-churn, and read-hot** (online sets, status per player),
shared across nodes. Redis is in-memory (microsecond ops), has the right data
structures (sets for "who's online" via `SADD`/`SREM`/`SCARD`), and supports TTLs for
auto-expiry. Putting it in Postgres would add write load and durability you don't need
for transient data.

**Q: Is Redis durable? Does it matter here?**
Redis offers RDB snapshots and AOF, but presence is disposable — if Redis restarts,
presence is wrong for a few seconds until events re-populate it (or you reconcile from
the log). So I don't rely on Redis durability; it's a cache/projection.

**Q: Redis pub/sub vs Kafka — why not just use Redis pub/sub as the bus?**
Redis pub/sub is fire-and-forget (no persistence, no replay, no consumer offsets) — a
subscriber that's down misses messages. Kafka persists, supports replay and consumer
groups with offsets. For a durable event backplane you want Kafka (or Redis
*Streams*, which adds persistence). Redis here is a presence store, not the bus.

**Q: How do you keep Redis presence consistent if a node crashes mid-update?**
Presence is eventually consistent and self-healing: it's rebuilt from `PlayerJoined/
Left` events, and you can add TTLs so stale "online" entries expire. The event log
remains the source of truth; Redis is a fast view.

### 13.8 Kafka & the distributed upgrade path

**Q: Walk me through moving the in-process bus to Kafka.**
Implement `core.Publisher` with a Kafka producer; the store publishes there after the
DB commit. Each consumer (presence, leaderboard, dashboard) becomes a **consumer
group**. **Partition by `room_id`** so per-room ordering is preserved. Consumers track
**offsets**; on restart they resume (or rebuild projections from the log). No consumer
*logic* changes — that's the payoff of coding to the interface now.

**Q: What are partitions, consumer groups, and offsets?**
A topic is split into partitions; ordering is guaranteed only within a partition.
A consumer group splits partitions among its members for parallelism (each partition
to one consumer in the group); different groups each get the full stream (fan-out). An
offset is a consumer group's position per partition, committed so it can resume.

**Q: How do you keep ordering with multiple partitions?**
You don't get global order — you get per-partition order. Choose the partition key so
things that must be ordered share a key. Here that's `room_id`: all of a room's events
stay ordered; cross-room order is irrelevant.

**Q: How would you avoid double-processing on consumer restart?**
Commit offsets *after* processing (at-least-once) and make consumers idempotent, or
use transactional/exactly-once semantics. Phoenix's projections are idempotent
(rebuild-from-log), so at-least-once is safe.

### 13.9 Distributed systems & scaling

**Q: Where does this sit on CAP?**
The write path favors **consistency** within a room (single-writer per room via
sticky routing + per-room ordering). The read side is **AP-leaning / eventually
consistent** (presence, leaderboards lag). That split — strong where the game needs
it, eventual where it doesn't — is the core trade-off.

**Q: What's "sticky routing" and why by room?**
Route all traffic for a room to the same node so that node owns the room's state and
ordering (single writer, no cross-node coordination per move). Key on `room_id`
(consistent hashing / a routing layer). It turns a hard distributed-consensus problem
into a local one.

**Q: How would you handle a node dying mid-match (host migration)?**
Because state is the fold of a durable log, another node can take over a room by
replaying its events from Postgres (and a snapshot) and accepting new connections.
Detect failure (health checks/leases), reassign the room key, clients reconnect (the
grace window helps), new node rehydrates from the log. Not implemented yet, but the
event-sourced design makes it tractable.

**Q: How do you make the system horizontally scalable?**
Stateless gateways behind a load balancer (any node serves any connection); room
servers sharded by `room_id`; shared Redis for presence; partitioned event store and
Kafka bus. Each tier scales independently. Nothing in the hot path requires a global
lock or global ordering.

**Q: Eventual consistency — how do you explain it to a user-facing concern?**
"Your move is instantly consistent (synchronous, authoritative). Your leaderboard
ranking updates a moment later." You design the UX around which data must be
read-after-write (the board) vs which can lag (stats), and you put each on the right
path.

### 13.10 Auth & security

**Q: Access token vs refresh token — why two?**
The access token is short-lived (15m) and **stateless** — verified by signature, no DB
hit per request, so it scales. But short life means it must be renewable without
re-login: the refresh token is long-lived, **stored server-side (hashed)**, and
exchanged for new access tokens. Short access TTL limits the blast radius of a leaked
access token; the refresh token can be revoked.

**Q: Why store refresh tokens hashed, and why rotate them?**
Hashed (SHA-256) so a DB leak doesn't expose usable tokens (same reason you hash
passwords). **Rotation = single use**: redeeming a refresh token deletes it and issues
a new pair, so a stolen refresh token is usable at most once and reuse is detectable.

**Q: JWT stateless vs server sessions — trade-offs?**
JWT: no per-request DB lookup, scales horizontally, but hard to revoke before expiry
(hence short TTL + revocable refresh). Server sessions: instantly revocable but a
lookup per request and shared session store. Phoenix uses short JWT access + revocable
refresh to get most of both.

**Q: How is the game itself secured against cheating?**
Server-authoritative validation: clients send intents, the reducer validates against
the rules before anything is persisted, illegal moves are rejected and never logged.
The client's board is just a render of server state. Add-ons (not done): rate
limiting, anomaly/cheat-detection hooks on the event stream.

**Q: What's missing security-wise?**
Rate limiting on the gateway, OAuth/password auth, TLS termination config, tighter WS
origin allow-list in prod, and per-action authorization audits. I call these out
rather than claim completeness.

### 13.11 Testing & correctness

**Q: How did you validate the chess engine?**
Unit tests for rules (castling, en passant, promotion, pins, mate/stalemate, FEN
round-trip) **plus perft** — counting the number of leaf nodes in the move tree to a
given depth and comparing to published reference values (start position depth 4 =
197,281; the Kiwipete position depth 3). Perft is the gold standard because a single
movegen bug changes the count. It's effectively a property-based correctness check.

**Q: How do you test concurrent code / the event loop?**
The hub has a unit test using the **in-memory store** (no DB) that drives intents
through and asserts state + that the log holds the right events, and that a fresh hub
**rebuilds identical state from the log** (the crash-recovery property). Run the suite
with `-race`. The in-memory store existing at all is what makes the engine testable in
isolation — a benefit of coding to the `EventStore` interface.

**Q: How would you load test it?**
Spin up many headless WS clients (the `cmd/smoke`/`cmd/seed` clients are the seed of
this), ramp concurrent rooms/connections, and measure p50/p99 move latency, events/s,
goroutine count, and GC with `pprof`. Find the per-room lock and DB append as the
first bottlenecks; validate the sticky-routing/Kafka plan against the numbers.

### 13.12 Curveballs / "what would you change"

**Q: If you rebuilt it, what would you do differently?**
Add snapshots from the start (replay-from-zero is a latent cost), make the bus durable
sooner if multi-node was a near-term goal, and add an outbox pattern so "append +
publish" is atomic across a crash (today a crash between commit and publish would drop
a live notification — harmless because consumers can rebuild from the log, but the
outbox makes it airtight).

**Q: What's the outbox pattern and do you need it?**
Write the event and an "to-publish" row in the *same* DB transaction, then a relay
publishes from the outbox and marks it done — guaranteeing "persisted ⟺ published"
even across crashes. I don't strictly need it because projections rebuild from the
log, but it's the correct answer for durable, exactly-once-ish delivery and I'd add it
with Kafka.

**Q: Biggest risk in this design?**
The per-room single-writer assumption: it makes correctness easy but means a hot room
is bounded by one node/one lock. Mitigation is fine for games (a room has ≤ a handful
of players); it would be wrong for a "millions in one room" use case, which would need
a different (CRDT/sharded-state) approach. Knowing the assumption and its limits is
the point.

**Q: Sell me on this being event-driven and not just "a server with a queue".**
Two things: (1) the **state itself** is event-sourced — it's not a side effect, it's
the source of truth, which gives replay/recovery/rebuildable read models; (2) services
are **decoupled by events** — the game emits `MatchEnded` and is completely unaware
that a leaderboard, presence tracker, or dashboard consume it. Adding the leaderboard
required zero changes to the game loop. That's event-driven architecture, not a queue
bolted onto CRUD.
