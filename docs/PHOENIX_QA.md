# Phoenix — 52-Question Interview Defense

Honest, code-grounded answers. **⚠️ flags real gaps** (aspirational vs implemented) so they're gradeable. Lead numbers are measured; caveats stated.

**Environment (for the benchmark answers):** Intel Core Ultra 7 165U laptop (12C/14T), Go 1.26.4, `GOMAXPROCS=14` (microbenchmarks are single-goroutine), local Docker `postgres:16-alpine` for append benchmarks (Neon PG18 in cloud). Microbenchmarks: Go `testing` harness (`go test -bench -benchmem`). End-to-end: custom open-loop WebSocket harness (`cmd/loadtest`).

---

## Architecture & Core Concept

**1. Phoenix in 30s. What problem does it solve?**
Every multiplayer game re-implements the same backend plumbing — auth, rooms, WebSockets, state sync, reconnection, persistence, replay, leaderboards. Phoenix is a reusable, event-sourced platform in Go where a developer writes **only their game rules as a reducer** and gets a full realtime backend. One binary hosts multiple games (turn-based chess + a real-time arena). It solves "stop rebuilding multiplayer infrastructure for every game."

**2. What is a reducer? Signature.**
`type Reducer func(state any, e Event) (newState any, err error)`. Given the current state and an incoming event, it returns the new state (or an error to reject the event). Registered via `game.OnEvent("move", reducer)`.

**3. Why the reducer pattern? Author vs platform.**
The author writes: `OnEvent` reducers (the rules), `InitialState`, optional `OnJoin/OnLeave`, `Derive` (lifecycle events), `RestoreState` (snapshot decode). The platform gives: auth, rooms, the WebSocket gateway, the append-only event log, the validate→append→reduce→broadcast loop, replay, snapshots, matchmaking, presence, leaderboard, and the admin dashboard. Chess is ~170 lines of rules; everything else is the platform.

**4. One binary, chess + arena — how does one engine serve turn-based and real-time?**
The hub is multi-game: it holds `map[gameType]Handlers` and picks the reducer set by the room's `game_type`. Both games are *just events through reducers* — the engine doesn't care about cadence. Chess emits a few events per minute; arena emits ~15/sec/player (position updates). ⚠️ **Honest:** arena has **no server-side tick/physics loop** — movement is client-sends-desired-position, server validates (clamp to field + cap step distance). So "real-time" here = high-frequency event-driven position updates, not a fixed-timestep authoritative simulation. That's a deliberate simplification I'd call out.

**5. One event end-to-end.**
Client sends `{type:"move", payload:{...}}` over WS → gateway routes to the room's hub → `dispatch` takes the per-room lock → runs the reducer (**validate**; error ⇒ reject, nothing persisted) → `store.Append` assigns a per-room `Seq` and durably writes (**append**), then publishes to the bus → sets new state (**reduce**) → marshals the delta and writes it to every connection in the room (**broadcast**) → releases the lock → emits any derived events through the same path.

**6. What's in an event vs state? Chess example.**
Event: `{ID, Seq, Type, RoomID, PlayerID, Payload(json), Timestamp, Version}` — e.g. `{Type:"move", Payload:{from:"e2",to:"e4"}}`. State is game-defined; chess: `{fen, turn, status, players:{w,b}, winner, lastMove, history[]}`. The event is the *fact that happened*; the state is the *fold of all facts*.

**7. Is the reducer pure? Why does purity matter?**
It must be **deterministic** — same (state, event) sequence ⇒ same result — because state is rebuilt by replaying the log; non-determinism would make replay diverge from live. ⚠️ **Honest:** chess reducers are pure (return new structs); the arena/sandbox reducers mutate maps **in place** for speed. That's not textbook-pure, but it's still deterministic, and I prevent the one hazard it creates (a concurrent read during broadcast serialization) by marshaling the broadcast **under the room lock** and never sharing state across rooms.

---

## Event Sourcing

**8. What is it / why over current-state?**
Persist an ordered, immutable log of events; current state = a left-fold over them. I chose it for four payoffs CRUD can't give: **replay/time-travel**, **server authority + audit** (the log is immutable legal history), **crash recovery/reconnection** (rebuild by replaying), and **rebuildable read models** (any projection recomputes from the log).

**9. Downsides / how handled.**
(a) Log grows unbounded → **snapshots** bound replay cost (but ⚠️ no pruning/compaction yet). (b) Reading "current state" needs a fold → I keep live state in memory per active room. (c) Eventual consistency of read models → accepted, game state stays synchronous. (d) Schema evolution → events carry a `Version` field (⚠️ but no upcasting logic implemented yet).

**10. Rebuild after restart.**
On first access to a room, `getOrCreate` → `rebuild`: load the latest snapshot (if any), `RestoreState` it, then `store.Load(room, snapshot.Seq+1, …)` and fold the tail. No snapshot ⇒ fold from Seq 1.

**11. Log growth / retention / compaction.**
Snapshots cap *replay* cost, but ⚠️ **retention/compaction is a gap** — old events aren't pruned today. Plan: partition the `events` table by room or time, archive cold partitions to object storage, and (since snapshots exist) safely truncate events older than the latest snapshot per room.

**12. Poison event during replay.**
⚠️ **Honest gap.** Today the rebuild loop applies `reducer`; if it errors, it **skips that event** and keeps prior state (doesn't crash). That's resilient but *silent*. Better: quarantine to a dead-letter table, alert, and continue — which I'd add. On the *write* path a bad event can't enter the log (validation rejects before append), so poison events are mostly a theoretical replay concern.

**13. Schema/versioning.**
Every event has a `Version` int. ⚠️ The **upcasting is not implemented** — currently all events are v1. The intended approach: reducers branch on `Version` (or an upcaster normalizes old payloads on load), and you never rewrite historical events. I'd flag this as designed-but-not-built.

---

## Snapshots & Recovery

**14. What's a snapshot / when taken?**
A materialized fold of a room's state at a given `Seq`, stored so recovery skips replaying from the start. **Event-count based**, per game: chess every 20 events, arena every 50. Taken inside `dispatch` when `Seq % N == 0`, encoded under the lock, written **async** off the hot path. (Not time-based — event-count maps directly to replay cost.)

**15. O(match) → O(1) recovery, concretely.**
Without snapshots, rebuilding folds *every* event — cost grows linearly with match length (O(match)). With snapshots, you restore the latest snapshot and fold only the **≤N-event tail** — constant regardless of match length (O(1) in match length).

**16. The 98% / 53× claim — how measured?**
`BenchmarkRehydrate` (Go bench, in-memory store + in-memory snapshot store). Baseline = fold all events; after = restore snapshot + fold a 50-event tail. At **5,000 events: 2,764,810 ns → 52,214 ns = 98% faster / 53×** (and 4,772 → 27 allocs). At 1,000 events: 90% / 10×. The win scales with match length because the baseline does, while the snapshot path stays flat.

**17. Where stored / corrupt snapshot?**
Postgres `snapshots` table (`room_id` PK, `seq`, `state` JSONB), upserted keeping the highest seq. **Corrupt/undecodable snapshot:** `RestoreState` returns `nil` on unmarshal error, and `rebuild` treats nil as "no snapshot" → **falls back to full replay from Seq 1**. So a bad snapshot degrades performance, never correctness — the event log remains the source of truth.

---

## Throughput & Benchmarks

**18. How measured — 148K ev/s/core?**
`BenchmarkMovePipeline` (Go `testing` harness). It calls the hub's `HandleMessage` in a tight single-goroutine loop: JSON-unmarshal intent → reducer → in-memory append (assign seq) → marshal broadcast → deliver to a no-op connection. **6,747 ns/op, 18 allocs/op → ~148K/sec.** ⚠️ **It's in-memory, bus disabled, no network** — it's the *engine CPU path*, not end-to-end. I now lead with the end-to-end number instead (see Q21/note).

**19. 148K of what, what hardware/Go/GOMAXPROCS?**
Lightweight move-like intents with a trivial reducer (not chess; chess's reducer alone is 6.4µs, so a chess move end-to-end is ~80–100K/core). Hardware: Intel Core Ultra 7 165U laptop, Go 1.26.4, `GOMAXPROCS=14`, but the bench is **single-goroutine** (one core).

**20. Why per-core? Linear across cores? Where does it break?**
Per-core because Go benches are single-goroutine — the honest unit. It scales ~linearly across cores for **many rooms** (each room has its own lock and runs independently). Linearity breaks (a) within a **single room** (serialized by one lock for ordering — single-core-bound by design), and (b) at shared resources: the event store, the bus, and GC.

**21. Bottleneck past 148K?**
In the microbench: **JSON (de)serialization + allocations** (18 allocs/op, dominated by marshaling the full-state broadcast). End-to-end the real wall is **durable writes** (see Q22). *The number I actually defend now is the end-to-end load test: **~12,000 ev/s at p99 < 4ms** (single node, batched store), which collapses to ~1K/s with the naive sync store — proof the durable write path is the bound.*

**22. ~50× / ~47K batched — before, and how COPY helps?**
Before = the synchronous store: one Postgres transaction + advisory lock + **fsync per event** → `BenchmarkAppendThroughput` measured **9.6 ms/op ≈ 104 events/sec** (local Docker PG16, single-threaded). Batched = **21,218 ns/op ≈ 47K/sec**: it (a) assigns `Seq` from an **in-memory counter** (no `SELECT MAX` round-trip), (b) buffers events and writes a batch with one bulk **`COPY`** (one commit/fsync amortized over hundreds of rows), (c) persists **async**. The ~50× is the per-commit fsync being amortized; the production-honest framing is ~10–50× and ~47K durable/sec.

**23. Batch size / flush interval / trade-off?**
**512 events or 2ms, whichever first.** Bigger batch ⇒ more throughput but higher latency and a bigger loss window; 2ms keeps p99 low while still amortizing the commit. It's a throughput-vs-latency-vs-durability dial; I picked low-latency defaults.

**24. Crash mid-batch — durability?**
⚠️ Buffered-but-unflushed events (≤512 / ≤2ms) are **lost on crash** — a bounded durability window. It's a *conscious trade-off offered alongside* the safe synchronous store (pick per need). Mitigations: events are also broadcast (clients hold them), flush-on-shutdown, and the real fix is an **outbox/WAL or Kafka** for exactly-once-ish. The sync store has **no** loss window.

---

## Pub/Sub Bus & CQRS

**25. CQRS — command vs query here?**
Command = an intent that produces an event (the write side: `move` → validated → appended). Query = a read model served by a purpose-built projection (leaderboard, presence, dashboard) built **asynchronously** from the event stream. Write and read paths are separate.

**26. Zero-allocation bus in a GC language — how?**
`InProcess.Publish` takes an `RLock`, iterates subscribers, and does a **non-blocking channel send** (`select { case ch<-e: default: drop+count }`). Channels are pre-allocated at subscribe time; the event is passed by value; nothing is allocated per publish. ⚠️ Nuance: the `Event` itself was allocated upstream (at append) — the *bus publish* adds zero.

**27. How verified zero-alloc?**
`BenchmarkPublishFanout` with `-benchmem`: **2,022 ns/op, 0 allocs/op** with 8 subscribers → ~495K msg/sec. `allocs/op == 0` is the proof.

**28. Server-authoritative — what/why?**
Clients send *intents*, never state. The server runs the reducer to **validate before appending**; illegal/out-of-turn events are rejected and never enter the log. Why: anti-cheat, consistency, and a log that only ever holds legal history (so replay is always valid).

**29. Projection lags/fails — what does the user see?**
The **game** is synchronous and authoritative, so gameplay is unaffected. A lagging projection means the **leaderboard/presence updates a beat late** (eventual consistency). If a projection process dies, the game keeps running (decoupled), and the projection **rebuilds from the log** on restart (the leaderboard literally recomputes from `MatchEnded` events).

**30. Kafka-swappable — show the abstraction.**
`core.Publisher { Publish(Event) }` (what the store publishes to) and `eventbus.Bus { Publish; Subscribe; Stats }`. `InProcess` and `RedisBus` both implement them; consumers only know `Bus.Subscribe`. A `KafkaBus` implementing the same two interfaces drops in with **zero consumer changes** — same seam as `EventStore` (in-memory ↔ Postgres).

**31. 2-node cluster — how do events cross nodes? Ordering?**
`RedisBus`: `Publish` does a Redis `PUBLISH`; every node `SUBSCRIBE`s and fans messages to its local subscribers. Verified: a game on node A delivered all events to node B's consumers (`events_published 0→11`). **Ordering:** per-room order is guaranteed by the **per-room `Seq` in the store** (the authoritative write side), not the bus; the bus is for *eventually-consistent* consumers. ⚠️ **Honest:** Redis pub/sub is fire-and-forget (no replay/offsets), and I demoed cross-node *event propagation*, **not** two players in one room on different nodes (that needs sticky routing + intent forwarding, which is designed, not built).

---

## Serialization

**32. Binary delta frames — delta against what?**
Instead of broadcasting full state every event, send just the **event** (the delta) encoded as **MessagePack**; the client applies it to its local copy. Baseline = the client's current state.

**33. ~80% smaller / 7× faster vs what, how measured?**
Vs full-state **JSON**. `BenchmarkBroadcastEncoding` at a 150-ply chess state: full-state JSON **1,413 B / 9,044 ns** vs delta+MessagePack **288 B / 1,266 ns** → ~80% smaller, ~7× faster; and delta stays constant as the match grows while full-state grows. ⚠️ **Honest:** this is built and benchmarked in `internal/wire`, but **not yet wired into the live broadcast path** — the running game still sends full-state JSON. So the 80% is a proven codec result, not yet a live-traffic result.

**34. How does the client apply a delta / missed frame?**
With deltas, the client applies each event in order. A **missed frame** is detected by a gap in `Seq` → request a resync (the server already sends a full **snapshot** on (re)connect). ⚠️ Since the live path currently sends full state every event, there's no miss problem live yet; gap-detection is part of the not-yet-wired delta path.

---

## Chess Engine Correctness

**35. What is perft / why does 197,281 prove correctness?**
Perft counts the leaf nodes of the move tree to depth N. From the start position depth 4 = **197,281** (a published reference). Any bug in move generation — a missing castle, a wrong en-passant, an illegal-move-not-filtered — changes the count. Matching the reference to depth 4 (plus the Kiwipete position to depth 3 = 97,862, which is dense in castling/EP/promotion/pins) is the gold-standard movegen check.

**36. Castling/EP/promotion/checks/pins? Tested?**
Yes — full legal move generation: castling (incl. can't castle through/into check, rights revoked on king/rook move), en passant, promotion (with SAN `=Q`), pins (a move leaving your king in check is filtered), check/checkmate/stalemate, SAN with disambiguation, FEN round-trip. Tested by 14 unit tests + perft. ⚠️ Draws = insufficient material only (no threefold/50-move).

---

## Concurrency & Safety

**37. Per-match loop single-threaded — why? Avoiding hot-path locks?**
Each room serializes all its events under **one mutex** held across validate→append→apply→broadcast. That gives a single, deterministic order (= the log order) with no per-field locking and no lock-ordering hazards. The "hot path" is one lock acquisition per event per room — cheap, and the simplicity is worth it.

**38. Multiple matches concurrently — goroutines/channels?**
Rooms are independent (own lock + state), so they run fully in parallel across cores. Goroutines: **two per connection** in the gateway (a read loop and a writer pump draining a buffered channel), plus **one per bus subscriber**. Dispatch itself runs on the caller's goroutine under the room lock — it's lock-per-room, not goroutine-per-match.

**39. Races you hit / how found?**
Yes — a **concurrent map read/write panic under load**: the sandbox/arena reducer mutates its state map in place, and I was marshaling the broadcast *after* releasing the lock, so a second event could mutate the map while JSON serialization read it. Found via a **server panic during the load test**. Fixed by **marshaling the broadcast under the room lock** (and making `CurrentState` return a detached encoded snapshot). ⚠️ I found it via the crash, not `-race`; I'd add `go test -race` to CI.

**40. Slow/disconnected client without blocking broadcast?**
Each connection has a **buffered send channel** drained by its own writer goroutine; the hub's `Send` is a **non-blocking** enqueue that **drops on a full buffer**. So a slow client never stalls the room's broadcast or other players; it resyncs from a snapshot on reconnect.

---

## Auth, Rooms, Matchmaking

**41. Auth — JWT or sessions?**
JWT. Short-lived **access tokens** (HS256, 15m, stateless) + long-lived **rotating refresh tokens** (stored hashed with SHA-256, single-use — redeeming one revokes it). Guest and email (passwordless) login. The WebSocket handshake authenticates via a `?token=` query param (browsers can't set WS headers).

**42. Rooms — created/scoped, events isolated?**
Rooms are rows in Postgres (`game_type`, status, owner, max players, invite code). Everything is keyed by `room_id`: the per-room `Seq`, the in-memory room runtime, the broadcast set, and event queries. A room only ever sees its own events.

**43. Matchmaking — pairing / queue?**
v1 = **quick-match "next open seat"**: the first caller creates a room and waits; the second is paired in. It's an in-memory waiting map per game type. ⚠️ **No skill/rating** yet — but it implements the `Matchmaker` interface, so a ranked/skill matcher drops in without touching callers.

**44. Reconnect flow.**
On socket drop the player is **detached but kept logically present** for a **30s grace window** (no `PlayerLeft` yet). Reconnecting within the window cancels the pending leave and the server resends a **snapshot** (rebuilt from log/snapshot). If the window elapses, `PlayerLeft` fires. So a network blip doesn't eject you mid-match.

---

## Reliability & Scale

**45. SPOF? How to make a match HA?**
⚠️ Today: single backend instance (+ single Postgres/Redis) — yes, a SPOF. HA path (designed, not built): because room state is **rebuildable from the log**, another node can take a room over by replaying it (host migration); that needs failure detection + leader election + sticky routing by `room_id`. The event-sourced design makes it tractable.

**46. Scale to 1M concurrent — what breaks first?**
Stateless gateways scale horizontally; shard rooms by `room_id` (sticky); Redis presence is shared; partition the event store; swap the in-process bus for Kafka. **First to break: the single Postgres write path** (need batching + partitioning/read-replicas) and the **single-node bus** (need Kafka). Then per-node connection limits (fd/memory tuning).

**47. Cheating / illegal events?**
Server-authoritative validation rejects illegal events before they persist — chess rejects illegal/out-of-turn moves; arena clamps position + caps step distance (anti-teleport). ⚠️ **Rate limiting is a gap** (no per-connection throttle yet) — that's the obvious next anti-abuse layer.

**48. Observability in prod?**
`/healthz`, `/admin/metrics` (players online, Redis presence, active rooms, per-room counts, `events_published`), the live Mission Control dashboard (events/sec chart, sessions, spectate), and structured logs. ⚠️ **No Prometheus/OpenTelemetry tracing yet** — I'd add `/metrics` (Prom) and OTel spans across the event path next.

---

## Design Trade-offs & Reflection

**49. Hardest bug?**
The concurrent-map-write panic under load (Q39): a reducer mutating state in place while the broadcast was serialized after the lock released. It only showed up under concurrency, surfaced as a crash during the load test, and the fix (serialize the broadcast inside the critical section) is a one-liner that's easy to get wrong. Runner-up: a deploy-time Neon auth failure caused by `channel_binding=require` that the Go driver doesn't negotiate.

**50. Rebuild differently?**
Wire the binary/delta codec into the **live** broadcast path (it's only benchmarked today); add snapshots and `-race` in CI from day 1; add an **outbox** so "append + publish" is atomic across a crash; give each game its own store (so arena's high-rate events don't share chess's); and load-test from a **separate box**.

**51. Why Go (not Node/Rust/Elixir)?**
Goroutines make tens of thousands of long-lived WS connections cheap and the per-room model simple; static binaries make deploy trivial; the stdlib has a production HTTP/WS server; GC is fine at this scale. Not Node (single-threaded event loop is awkward for CPU-bound reducers like chess validation). Not Rust (slower iteration for a fast-moving platform; perf wasn't the constraint). ⚠️ **Honest:** **Elixir/BEAM (Phoenix Channels)** is arguably the *best* fit for this problem — per-process isolation, supervision, distribution built in. I chose Go for static-binary ops simplicity, ecosystem familiarity, and raw per-core throughput, and I'd happily defend Elixir as the strong alternative.

**52. Least confident part?**
The **distributed/multi-node story**: the Redis bus is fire-and-forget (no offsets/replay), cross-node *single-room* play isn't built, and cross-node ordering leans entirely on the per-room `Seq` rather than the transport. Close second: the **batched store's durability window** and the fact the **delta codec isn't live yet**. Those are exactly where I'd focus next, and I'd rather name them than oversell.

---

### Headline numbers to lead with (all measured)
- **~12,000 events/sec end-to-end at p99 < 4ms** (single node, batched store, open-loop WS load test) — conservative floor (generator shared the box).
- **~20× over the naive per-event-commit path** (which collapsed at ~1K/s).
- **98% / 53× faster recovery** with snapshots (5,000-event match).
- **Perft-validated** chess engine (197,281 nodes, depth 4).
- Deeper-dive (if asked): in-engine CPU path ~148K/core; zero-alloc bus ~495K msg/s; delta+binary codec ~80% smaller / 7× faster (benchmarked, not yet live).
