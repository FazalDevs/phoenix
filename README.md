# Phoenix

A reusable, event-sourced **multiplayer backend platform** in Go. Write only your
game rules; Phoenix handles auth, rooms, WebSockets, state sync, persistence,
replay, presence, leaderboards, and scaling.

```go
game := phoenix.New(cfg)
game.OnJoin(playerJoined)
game.OnEvent("move", movePiece)
game.Run() // boots the full backend: WS, rooms, event store, dashboard
```

> Import the SDK, add your rules, `docker build`, deploy. The same binary is a
> complete multiplayer backend for your game.

## Core idea: everything is an event

Phoenix never mutates state. A client sends an *intent*; the server appends an
**event** to an append-only log; a **reducer** folds it into new state; the delta
is broadcast. The full log makes every match **replayable** and the server
**authoritative** (clients can't cheat by mutating state).

```
Client → Gateway → Event Log → Reducer → State → Broadcast → Clients
```

## Event-driven architecture (the read side)

The write path above is **synchronous and authoritative** — a game needs
low-latency, in-order, server-validated delivery to the players in the room.
Everything *derived* runs **asynchronously off an event bus**:

```
                          ┌── presence  (Redis projection)        ← PlayerJoined/Left
store.Append(event) ──▶ Event Bus ──┼── leaderboard (Postgres read model)   ← MatchEnded
   (durable first)         (pub/sub) ├── dashboard live stream (per-room)
                          └── metrics  (event throughput)
```

- **One bus, many consumers.** Services never call each other — they react to
  events. Adding a feature = adding a subscriber, not editing the core.
- **CQRS read models.** The leaderboard is a projection *folded from
  `MatchEnded` events*. It is **rebuildable**: on boot it recomputes from the log,
  so it is always exactly the fold of history and can never drift.
- **Derived domain events.** A move that delivers checkmate emits a `MatchEnded`
  event (via `Engine.Derive`); the game loop has no idea the leaderboard exists.
- **Decoupled failure.** A slow or down consumer (e.g. Redis offline) never
  blocks a game — the write path doesn't wait on the bus.
- **Swappable transport.** `eventbus.InProcess` is one implementation of the
  `Publisher`/`Bus` seam. A Kafka/NATS producer drops in for multi-node fan-out
  (partition by room) with **zero consumer changes** — the same reason the
  `EventStore` can move Postgres → Kafka+Postgres.

Why sync writes but async reads? Game correctness (ordering, authority, latency)
demands synchronous; presence/leaderboards/dashboards are eventually consistent
and must not slow the game. Keeping that boundary explicit is the design.

## Architecture (Phase 1)

| Concern        | Implementation                              |
| -------------- | ------------------------------------------- |
| Event store    | Postgres append-only `events` table         |
| Presence/cache | Redis                                       |
| Transport      | WebSocket (coder/websocket) + REST          |
| Auth           | JWT access + rotating refresh, guest login  |
| Extensibility  | `EventStore`, `Matchmaker`, `PresenceStore` interfaces — swap any implementation |

Plugin interfaces live in [pkg/phoenix/plugins.go](pkg/phoenix/plugins.go): the
core depends on interfaces only, so storage can move from Postgres → Kafka+PG for
distributed fan-out (Phase 3) without engine changes.

## Quick start — the chess demo

Chess is a full multiplayer game written entirely on the SDK ([cmd/chess](cmd/chess/main.go),
~170 lines of rules). The platform supplies everything else.

```bash
cp .env.example .env
docker compose up -d                       # Postgres + Redis (host ports 55432 / 63790)

# backend (the chess game server)
go run ./cmd/chess                          # :8080  (PORT to override)

# dashboard + game client
cd web && cp .env.local.example .env.local  # point NEXT_PUBLIC_API_URL at the backend
npm install && npm run dev                  # :3000

# optional: seed a finished game to replay
go run ./cmd/seed                           # plays a Scholar's-mate match over real WebSockets
```

Then open:
- **`/`** — landing page
- **`/play`** — play chess (guest login → Quick Match → live board). Open two tabs to play yourself.
- **`/admin`** — live metrics + rooms
- **`/admin/rooms/{id}`** — live event stream + **replay scrubber** (time-travel any match)

## How a game is built on Phoenix

```go
game := phoenix.New(phoenix.WithGameType("chess"))
game.InitialState(func() any { return newChessState() })
game.OnEvent("move", moveReducer)   // validate vs rules engine, fold into new state
game.Run()                          // boots auth, rooms, WS, event store, replay, dashboard
```

The `move` reducer rejects illegal/out-of-turn moves, so the server is authoritative
and the append-only log only ever holds legal history — which is exactly what makes
replay and reconnection correct.

## Layout

```
cmd/chess          chess game server (built on the SDK) — the flagship demo
cmd/phoenix        generic reference server
cmd/seed           seeds a demo chess match
cmd/smoke          raw-protocol E2E client
pkg/phoenix        public SDK (Engine, Event, plugin interfaces)
internal/chess     perft-validated chess rules engine
internal/          auth, room, gateway, state, store, matchmaking, admin
deploy/            docker-compose, migrations, k8s (later)
web/               Next.js app: landing + chess client + admin dashboard
```

## Benchmarks

Measured in-process with the Go benchmark harness (`go test -bench=. ./...`),
single core (Intel Core Ultra 7 165U). Rooms are independent, so throughput scales
~linearly across cores.

| Benchmark | ns/op | ~ops/sec/core | allocs/op |
| --- | --- | --- | --- |
| Full event pipeline (parse → validate → append → reduce → marshal) | 6,747 | ~148,000 | 18 |
| Chess move validation (FEN parse + legality) | 6,443 | ~155,000 | 10 |
| Event-bus fan-out, 8 consumers | 2,022 | ~495,000 | **0** |

**Batched write-path optimization** — the per-event transactional store pays a
commit/fsync per event; the batched store assigns `seq` in memory and flushes
events with a single bulk `COPY`, persisting asynchronously (a configurable,
bounded durability window — offered alongside the safe synchronous store):

| Event store | ns/op (durable) | ~events/sec |
| --- | --- | --- |
| Sync (txn + advisory lock + fsync per event) | 9,601,976 | ~104 |
| Batched async (in-mem seq + bulk COPY) | 21,218 | **~47,000** |

(~450× on this Docker/fsync baseline; **~10–50× and ~47K durable events/sec** is
the production-realistic framing — the gap is per-commit fsync, which batching
amortizes.)

**Snapshot rehydration optimization** — on reconnect / crash recovery, restore the
latest snapshot and fold only the tail (`O(1)`) instead of replaying the whole
event log (`O(match length)`):

| Match length | Full replay | Snapshot + tail | Improvement |
| --- | --- | --- | --- |
| 1,000 events | 237,559 ns / 766 allocs | 23,239 ns / 27 allocs | **90% faster (10×)** |
| 5,000 events | 2,764,810 ns / 4,772 allocs | 52,214 ns / 27 allocs | **98% faster (53×), 99% fewer allocs** |

**Binary protocol + delta broadcast** — send only the changed event (the client
applies it) instead of the full state, and encode it as MessagePack instead of
JSON. Delta size is constant; full-state grows with match history (150-ply game):

| Strategy | Payload | Serialize |
| --- | --- | --- |
| Full-state JSON (default) | 1,413 B | 9,044 ns |
| Delta + MessagePack | 288 B | 1,266 ns |

→ **~80% smaller payloads, ~7× faster serialization**, and flat regardless of
match length (see [internal/wire](internal/wire/codec.go)).

**Horizontal scaling (multi-node)** — `PHOENIX_BUS=redis` swaps the in-process bus
for a Redis-backed one ([internal/eventbus/redis.go](internal/eventbus/redis.go))
so events fan out cluster-wide. Verified with a 2-node cluster: a game played on
node A delivered all its events to node B over Redis (node B's `events_published`
went 0 → 11) and updated node B's leaderboard. Same `Bus` interface, zero consumer
changes — the seam designed in from day one.

Correctness: the chess engine is **perft-validated** (start position to depth 4 =
197,281 nodes; Kiwipete to depth 3). Reproduce with:

```bash
go test -bench=Benchmark -benchmem ./internal/state/ ./internal/chess/ ./internal/eventbus/
```

## Roadmap

- **Phase 1** (now): auth, rooms, WS, event→reducer→broadcast, SDK, admin dashboard, Chess demo
- **Phase 2**: presence, replay, snapshots, leaderboards, chat, metrics, Skribbl demo
- **Phase 3**: matchmaking, distributed rooms, gRPC, K8s, Agar.io demo
- **Phase 4**: SDK polish, plugin system, replay inspector, room migration
