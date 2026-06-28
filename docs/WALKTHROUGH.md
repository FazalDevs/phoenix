# Phoenix — Complete App Walkthrough & Context

A from-zero explanation of the whole system, plus a component/contract reference.
Read Parts 1–3 to *understand* it; use Parts 4–11 as reference. If you can narrate
**Part 3** (one move through the system) and **Part 2** (event → reducer → loop),
you can defend the app.

---

## Part 1 — The one idea

Every multiplayer game rebuilds the same backend plumbing — accounts, connections,
rooms, state sync, reconnection, persistence, replay, leaderboards. That work is
hard, error-prone, and unrelated to the actual game.

**Phoenix is a reusable backend for multiplayer games.** A developer writes only
their game's *rules* and gets a complete, deployable multiplayer backend. The same
backend runs chess, a real-time arena, or anything else.

> Like Express/Rails for the web — you don't rebuild HTTP routing per app. With
> Phoenix you don't rebuild multiplayer infrastructure per game.

---

## Part 2 — The mental model (3 concepts)

**1. Everything is an event.** We don't store "the current board." We store the
ordered list of things that happened (`PlayerJoined`, `PlayerMoved`, `MatchEnded`).
Current state is those events folded together. (Like a bank storing transactions,
not just the balance.)

**2. A reducer turns events into state.** One function:
`reducer(currentState, event) → newState`. The developer writes only this. For
chess: "given the board and a `move` event, validate it and return the new board."

**3. The loop: validate → append → reduce → broadcast.** Every player action runs
these four steps. This is the spine of the entire platform:
1. **Validate** — is it legal? (run the reducer; error ⇒ reject)
2. **Append** — write the event to the append-only log
3. **Reduce** — fold it into new state
4. **Broadcast** — push the update to everyone in the room

---

## Part 3 — One chess move through the whole system

Player drags a pawn e2→e4. (Narrate this and you've defended the app.)

1. **Browser** has a WebSocket (persistent 2-way pipe) open to the backend and
   sends `{ "type":"move", "payload":{"from":"e2","to":"e4"} }`.
2. **Gateway** ([internal/gateway](../internal/gateway/gateway.go)) receives it on
   that player's (already-authenticated) connection and routes it to the room.
3. **Hub** ([internal/state/hub.go](../internal/state/hub.go)) takes the room's lock
   and runs the loop:
   - **Validate:** calls the chess reducer → chess rules engine → "is e2→e4 legal,
     and is it this player's turn?" If not, **reject** — nothing saved, error sent
     back. (This is why cheating is impossible: the *server* decides.)
   - **Append:** the legal move is written to the **event store** (Postgres) with a
     per-room sequence number.
   - **Reduce:** the new board (FEN, turn, status) is computed.
   - **Broadcast:** the update is serialized once and sent to every connection in
     the room — both players and spectators.
4. **Both browsers** re-render. Typically a few milliseconds.
5. **Asynchronously:** the appended event is also **published to the event bus**.
   Background services react — if it was checkmate, a `MatchEnded` event fires and
   the **leaderboard** updates. This never slows the move down.

---

## Part 4 — Components (what & why)

| Component | Package | What it does / why |
| --- | --- | --- |
| **SDK** | [pkg/phoenix](../pkg/phoenix/engine.go) | What the game dev touches: `New()`, `game.OnEvent("move", reducer)`, `Run()`. `Run()` boots everything below. |
| **Gateway** | [internal/gateway](../internal/gateway/gateway.go) | WebSocket lifecycle: upgrade, auth handshake, heartbeat, **30s reconnect grace**. |
| **Auth** | [internal/auth](../internal/auth/service.go) | Guest + email login, JWT access tokens, rotating refresh tokens. Frictionless. |
| **Rooms** | [internal/room](../internal/room/service.go) | A room = one game session; everything scoped by room ID. |
| **Matchmaking** | [internal/matchmaking](../internal/matchmaking/matchmaker.go) | v1 "next open seat" pairing; behind a `Matchmaker` interface. |
| **Hub / state engine** | [internal/state](../internal/state/hub.go) | Runs validate→append→reduce→broadcast, one room at a time. The heart. Multi-game. |
| **Event store** | [internal/store](../internal/store/postgres.go) | Append-only event log (Postgres). System of record. Memory + Batched variants. |
| **Event bus** | [internal/eventbus](../internal/eventbus/bus.go) | Pub/sub; after append, events are published; services react. In-process + Redis. |
| **Presence** | [internal/presence](../internal/presence/redis.go) | Who's online (Redis projection); reacts to join/leave. |
| **Leaderboard** | [internal/leaderboard](../internal/leaderboard/leaderboard.go) | Wins/losses (Postgres projection); reacts to `MatchEnded`; rebuildable from the log. |
| **Admin / Mission Control** | [internal/admin](../internal/admin/http.go) | Metrics, live spectate, leaderboard, "launch demo bots". |
| **Chess rules engine** | [internal/chess](../internal/chess/) | Pure chess logic; perft-validated. A library the chess reducer calls. |
| **Games** | [internal/games](../internal/games/) | `chess` + `arena` registered on the SDK. |
| **Bots** | [internal/bots](../internal/bots/bots.go) | Server-side demo players so the dashboard is always live. |

---

## Part 5 — Where data lives (and why three stores)

- **Postgres** — durable, queryable system of record: the **event log**, users,
  rooms, leaderboard, snapshots.
- **Redis** — fast, ephemeral: **presence**. Disposable; rebuilds from events.
- **In-memory (Go)** — the *live* state of active rooms, so you don't refold the log
  per move. Rebuilt from Postgres on room wake-up.

"Right tool per job" is the one-line answer to "why three stores?"

---

## Part 6 — Data model & contracts (reference)

**Postgres tables** ([deploy/migrations](../deploy/migrations/)):
- `users(id, email, is_guest, display_name, banned, created_at)`
- `refresh_tokens(token_hash PK, user_id, expires_at)` — hashed, rotated
- `rooms(id, owner_id, game_type, status, max_players, is_private, invite_code, created_at)`
- `events(id, room_id, seq, type, player_id, payload JSONB, version, timestamp, UNIQUE(room_id, seq))` — **append-only**
- `leaderboard(player_id PK, wins, losses, draws)` — rebuildable projection
- `snapshots(room_id PK, seq, state JSONB, created_at)`

**Redis:** `presence:online` (set), `presence:status:<id>` (string).

**WebSocket protocol** (server→client): `{type:"snapshot", room_id, state}` on
connect; `{type:"event", room_id, event, state}` per event; `{type:"error", error}`
on rejection. Client→server: `{type:"<eventType>", payload:{...}}`.

**Key REST:** `POST /login`, `POST /matchmake?game=`, `GET /admin/metrics`,
`GET /admin/rooms`, `GET /admin/rooms/{id}/events`, `GET /leaderboard`,
`POST /admin/demo?bots=`, `GET /healthz`, `WS /ws?room=&token=`.

---

## Part 7 — Two games, one engine

The hub holds a map of `game_type → reducers`; a room's `game_type` picks the
rules. **One binary hosts both** chess (turn-based) and arena (real-time: players
move dots, eat food). This is the proof it's a *platform*: identical infrastructure,
only the reducers differ.

> Honest note: arena movement is client-sends-position, server-validates-and-clamps
> (max-step anti-teleport) — there is **no server physics tick**. Say so if probed.

---

## Part 8 — Optimizations (what / why / numbers)

1. **Batched async writes → ~20×, ~12K events/sec end-to-end at p99 < 4ms.**
   Problem: each event was one Postgres transaction + fsync (~600/s ceiling). Fix:
   assign seq in memory (skip a round-trip) + write batches via bulk `COPY` (one
   fsync per hundreds of rows) + persist async. Trade-off: a crash can lose the last
   unflushed batch (bounded window); offered alongside the safe per-event store.
   Measured with an open-loop WebSocket load test ([cmd/loadtest](../cmd/loadtest/main.go)).

2. **Snapshots → 98% (53×) faster recovery.** Problem: rebuilding a room replayed
   every move (`O(match)`). Fix: snapshot periodically; on rebuild, restore the
   latest snapshot and fold only the tail (`O(1)`). Measured: 5,000-event match
   2.76ms → 52µs (`BenchmarkRehydrate`).

3. **Zero-allocation event bus** (`0 allocs/op`) — publish creates no garbage, so
   the bus never causes GC pressure or becomes the bottleneck.

**Number hierarchy:** lead with **~12K ev/s end-to-end @ p99<4ms** (real load test);
the in-engine ~148K/core and bus throughput are component microbenchmarks — mention
only if asked, and label them as such.

---

## Part 9 — Patterns, named

- **Event sourcing** — store the event log; state = fold. Gives replay, audit,
  crash recovery, rebuildable read models.
- **CQRS** — separate the write path (commands → events) from the read path
  (projections: leaderboard, presence, dashboard).
- **Server-authoritative** — clients send intents, never state; server validates.
  Anti-cheat + consistency.
- **Event-driven** — services react to events via the bus, not direct calls. Adding
  the leaderboard required zero changes to the game.

---

## Part 10 — Deployment

One Go binary ([cmd/server](../cmd/server/main.go)) in a Docker image on **Render**;
Postgres on **Neon**, Redis on **Upstash**, Next.js frontend on **Vercel**. The
backend **self-migrates** on boot (creates tables on a blank DB). Scales by running
more instances + sharding rooms across them (and swapping the in-process bus for
Redis/Kafka — designed for via the `Bus`/`Publisher` interfaces).

Live: https://phoenix-blush-eight.vercel.app — `/play` (chess), `/arena`, `/admin`.

---

## Part 11 — Honest limitations (own these)

- Arena has no server tick (client-driven movement, server-clamped).
- Event-log retention/compaction not implemented (snapshots bound replay, not size).
- Schema versioning: `Version` field exists, upcasting not implemented.
- Poison-event-on-replay: currently skipped silently (would add a dead-letter).
- Distributed: Redis bus is fire-and-forget; verified cross-node *event propagation*,
  not two players in one room across nodes. Kafka is interface-ready, not built.
- Binary/delta codec is benchmarked, not yet wired into the live broadcast path.
- No rate limiting; no OpenTelemetry tracing yet.
- HA/host-migration designed (state is rebuildable) but not implemented.

---

## Part 12 — 90-second narration

> "Phoenix is a reusable multiplayer game backend in Go — a developer writes only
> their game rules as a reducer and gets a full realtime backend: auth, rooms,
> WebSockets, persistence, replay, leaderboards. The core idea is event sourcing:
> every action is an immutable event in an append-only log, and game state is the
> fold of those events — which gives replay, crash recovery, and an authoritative,
> cheat-proof server for free. The write path is synchronous and authoritative —
> validate, append, reduce, broadcast — and everything derived (leaderboards,
> presence, dashboards) runs asynchronously off an event bus, which is CQRS. I
> proved it's a platform by running two very different games on one binary —
> turn-based chess and a real-time arena. And I optimized the core: batched async
> writes took durable throughput ~20× to about 12K events/sec end-to-end at sub-4ms
> p99, and state snapshots cut crash-recovery ~98% by replaying only the tail of the
> log instead of the whole match."
