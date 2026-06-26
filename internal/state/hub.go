// Package state is the runtime heart of Phoenix: the event -> reducer -> state
// -> broadcast loop. Developers never mutate state; they send intents that
// become events, which reducers fold into new state. Because state is a pure
// fold over the append-only log, any room can be rebuilt from scratch — that is
// what powers reconnect, crash recovery, and replay.
package state

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/fazal/phoenix/internal/core"
	"github.com/google/uuid"
)

// Conn is a live client connection (implemented by the WS gateway). The hub
// only needs to identify and write to it.
type Conn interface {
	ConnID() string
	PlayerID() string
	DisplayName() string
	RoomID() string
	Send(msg []byte)
}

// Handlers is the developer-supplied game logic, registered via the SDK.
type Handlers struct {
	Reducers  map[string]core.Reducer // eventType -> reducer
	InitState func() any              // fresh state for a new room
	OnJoin    func(core.Player)
	OnLeave   func(core.Player)
	// Derive inspects each applied state transition and may emit server-authored
	// domain events (e.g. MatchStarted/MatchEnded). Those events flow through the
	// same persist -> publish -> broadcast pipeline so consumers react to them.
	Derive core.Deriver

	// Snapshots bound rehydration cost. When set (with Restore + SnapshotEvery),
	// a room restores its latest snapshot and folds only the tail of events
	// instead of replaying the entire log from Seq 1.
	Snapshots     core.SnapshotStore
	Restore       func([]byte) any // decode a snapshot's JSON back into typed state
	SnapshotEvery int64            // take a snapshot every N events (0 = disabled)
}

// Hub owns all live room runtimes for one game.
type Hub struct {
	store    core.EventStore
	handlers Handlers
	mu       sync.Mutex
	games    map[string]*game // roomID -> runtime
}

func NewHub(store core.EventStore, h Handlers) *Hub {
	if h.Reducers == nil {
		h.Reducers = map[string]core.Reducer{}
	}
	if h.InitState == nil {
		h.InitState = func() any { return map[string]any{} }
	}
	return &Hub{store: store, handlers: h, games: make(map[string]*game)}
}

// game is the per-room runtime: members + current state, guarded by a mutex so
// events for one room apply in a single serialized order.
type game struct {
	roomID string
	hub    *Hub
	mu     sync.Mutex
	state  any
	conns  map[string]Conn
}

// getOrCreate returns the runtime for roomID, rebuilding its state from the
// event log on first access (this is reconnect/crash recovery).
func (h *Hub) getOrCreate(ctx context.Context, roomID string) *game {
	h.mu.Lock()
	g, ok := h.games[roomID]
	if !ok {
		g = &game{roomID: roomID, hub: h, state: h.handlers.InitState(), conns: make(map[string]Conn)}
		h.games[roomID] = g
	}
	h.mu.Unlock()

	if !ok {
		g.rebuild(ctx) // replay prior events to restore state
	}
	return g
}

// rebuild restores room state from the log. With snapshots configured it loads
// the latest snapshot and folds only the events after it (O(tail)); otherwise it
// folds the entire log from Seq 1 (O(history)). Idempotent because reducers are
// pure functions of (state, event).
func (g *game) rebuild(ctx context.Context) {
	h := g.hub
	from := int64(1)

	g.mu.Lock()
	defer g.mu.Unlock()

	// Fast path: start from the latest snapshot instead of Seq 1.
	if h.handlers.Snapshots != nil && h.handlers.Restore != nil {
		if snap, ok, err := h.handlers.Snapshots.Latest(ctx, g.roomID); err == nil && ok {
			if st := h.handlers.Restore(snap.State); st != nil {
				g.state = st
				from = snap.Seq + 1
			}
		}
	}

	events, err := h.store.Load(ctx, g.roomID, from, 0)
	if err != nil {
		return
	}
	for _, e := range events {
		if r := h.handlers.Reducers[e.Type]; r != nil {
			if ns, err := r(g.state, e); err == nil {
				g.state = ns
			}
		}
	}
}

// Join attaches a connection: records membership and sends a state snapshot. On
// a fresh join it also fires OnJoin and persists a PlayerJoined event. On a
// reconnect within the grace window (isReconnect=true) it only re-attaches and
// re-snapshots — no duplicate join event — because the player never logically
// left.
func (h *Hub) Join(ctx context.Context, c Conn, isReconnect bool) error {
	g := h.getOrCreate(ctx, c.RoomID())

	g.mu.Lock()
	g.conns[c.ConnID()] = c
	state := g.state
	g.mu.Unlock()

	if !isReconnect {
		player := core.Player{ID: c.PlayerID(), DisplayName: c.DisplayName(), RoomID: c.RoomID()}
		if h.handlers.OnJoin != nil {
			h.handlers.OnJoin(player)
		}
		// Persist join as an event (server-authoritative history) and apply it.
		payload, _ := core.NewPayload(player)
		_ = h.dispatch(ctx, g, c.PlayerID(), "PlayerJoined", payload, false, false)
	}

	// Send the (re)joining client the current snapshot so it renders immediately.
	c.Send(mustJSON(outbound{Type: "snapshot", RoomID: c.RoomID(), State: state}))
	return nil
}

// Detach removes a dead socket from its room without firing any event. Used the
// instant a connection drops; the player remains logically present so they can
// reconnect within the grace window.
func (h *Hub) Detach(c Conn) {
	h.mu.Lock()
	g := h.games[c.RoomID()]
	h.mu.Unlock()
	if g == nil {
		return
	}
	g.mu.Lock()
	if cur, ok := g.conns[c.ConnID()]; ok && cur == c {
		delete(g.conns, c.ConnID())
	}
	g.mu.Unlock()
}

// Leave is the logical departure: fires OnLeave and persists a PlayerLeft event.
// The gateway calls this only after the reconnect grace window elapses with no
// reconnection.
func (h *Hub) Leave(ctx context.Context, player core.Player) {
	h.mu.Lock()
	g := h.games[player.RoomID]
	h.mu.Unlock()
	if g == nil {
		return
	}
	if h.handlers.OnLeave != nil {
		h.handlers.OnLeave(player)
	}
	payload, _ := core.NewPayload(player)
	_ = h.dispatch(ctx, g, player.ID, "PlayerLeft", payload, false, false)

	// Drop idle room runtimes; they rebuild from the log on next join.
	g.mu.Lock()
	empty := len(g.conns) == 0
	g.mu.Unlock()
	if empty {
		h.mu.Lock()
		delete(h.games, player.RoomID)
		h.mu.Unlock()
	}
}

// inbound is the client->server message (an intent).
type inbound struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// outbound is the server->client message.
type outbound struct {
	Type   string      `json:"type"` // snapshot | event | error
	RoomID string      `json:"room_id"`
	Event  *core.Event `json:"event,omitempty"`
	State  any         `json:"state,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// HandleMessage processes one client intent: validate, persist as event, reduce,
// broadcast. This is the full Move -> Event -> Reducer -> New State -> Broadcast
// pipeline for a single message.
func (h *Hub) HandleMessage(ctx context.Context, c Conn, raw []byte) {
	var in inbound
	if err := json.Unmarshal(raw, &in); err != nil || in.Type == "" {
		c.Send(mustJSON(outbound{Type: "error", Error: "malformed message"}))
		return
	}
	h.mu.Lock()
	g := h.games[c.RoomID()]
	h.mu.Unlock()
	if g == nil {
		return
	}
	if _, ok := h.handlers.Reducers[in.Type]; !ok {
		c.Send(mustJSON(outbound{Type: "error", Error: "unknown event type: " + in.Type}))
		return
	}
	// validate=true: a reducer that returns an error rejects the intent BEFORE
	// anything is persisted (server-authoritative — e.g. an illegal chess move).
	if err := h.dispatch(ctx, g, c.PlayerID(), in.Type, in.Payload, true, false); err != nil {
		c.Send(mustJSON(outbound{Type: "error", Error: err.Error()}))
	}
}

// dispatch is the core loop: build event, fold via reducer, persist, broadcast.
// The whole body holds g.mu so a room's events are validated, appended, and
// applied in one serialized order — the append-only log order always equals the
// state-mutation order. When validate is true a reducer error rejects the intent
// and nothing is written; server-originated events (validate=false) always
// persist, keeping prior state if their reducer happens to error.
func (h *Hub) dispatch(ctx context.Context, g *game, playerID, eventType string, payload json.RawMessage, validate, derived bool) error {
	g.mu.Lock()

	prev := g.state
	e := &core.Event{
		ID:       uuid.NewString(),
		Type:     eventType,
		RoomID:   g.roomID,
		PlayerID: playerID,
		Payload:  payload,
	}

	newState := prev
	if r := h.handlers.Reducers[eventType]; r != nil {
		ns, err := r(prev, *e)
		if err != nil {
			if validate {
				g.mu.Unlock()
				return err // reject: nothing persisted, state untouched
			}
			// server event: persist it but keep prior state
		} else {
			newState = ns
		}
	}

	// Append assigns Seq and durably persists (the system of record). The store
	// then publishes to the bus for async consumers.
	if err := h.store.Append(ctx, e); err != nil {
		g.mu.Unlock()
		return err
	}
	g.state = newState

	conns := make([]Conn, 0, len(g.conns))
	for _, c := range g.conns {
		conns = append(conns, c)
	}

	// Serialize the broadcast payload WHILE holding the room lock. newState may
	// alias state a reducer mutates in place, so marshaling here (not after
	// unlock) prevents a concurrent dispatch from mutating it mid-serialization.
	msg := mustJSON(outbound{Type: "event", RoomID: g.roomID, Event: e, State: newState})

	// Compute derived domain events from this transition. Only original events
	// derive (derived==false), so emitted events never recurse.
	var derivedEvents []core.DerivedEvent
	if !derived && h.handlers.Derive != nil {
		derivedEvents = h.handlers.Derive(prev, newState, *e)
	}

	// If a snapshot is due, capture the encoded state under the lock (so it's a
	// consistent point-in-time copy); persist it asynchronously off the hot path.
	var snapBytes []byte
	if h.handlers.Snapshots != nil && h.handlers.SnapshotEvery > 0 && e.Seq%h.handlers.SnapshotEvery == 0 {
		snapBytes = mustJSON(newState)
	}
	g.mu.Unlock()

	// Broadcast the authoritative delta to the room's players synchronously
	// (low latency, in order). Observers/projections get it via the bus.
	for _, c := range conns {
		c.Send(msg)
	}

	if snapBytes != nil {
		seq := e.Seq
		go func() {
			_ = h.handlers.Snapshots.Save(context.Background(),
				core.Snapshot{RoomID: g.roomID, Seq: seq, State: snapBytes})
		}()
	}

	// Emit derived events through the same pipeline (persist -> publish ->
	// broadcast). They are server-authored (no player id) and don't re-derive.
	for _, d := range derivedEvents {
		_ = h.dispatch(ctx, g, "", d.Type, d.Payload, false, true)
	}
	return nil
}

// Stats is a live snapshot of hub activity for the metrics dashboard.
type Stats struct {
	ActiveRooms   int            `json:"active_rooms"`
	OnlinePlayers int            `json:"online_players"`
	PerRoom       map[string]int `json:"per_room"` // roomID -> connection count
}

// Stats returns current live membership counts across all rooms.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	games := make([]*game, 0, len(h.games))
	for _, g := range h.games {
		games = append(games, g)
	}
	h.mu.Unlock()

	s := Stats{PerRoom: make(map[string]int)}
	seen := make(map[string]struct{})
	for _, g := range games {
		g.mu.Lock()
		n := len(g.conns)
		for _, c := range g.conns {
			seen[c.PlayerID()] = struct{}{}
		}
		g.mu.Unlock()
		if n > 0 {
			s.ActiveRooms++
			s.PerRoom[g.roomID] = n
		}
	}
	s.OnlinePlayers = len(seen)
	return s
}

// CurrentState returns a copy-by-reference of a room's current reduced state,
// for the admin live-view. nil if the room is not currently active in memory.
func (h *Hub) CurrentState(roomID string) (any, bool) {
	h.mu.Lock()
	g := h.games[roomID]
	h.mu.Unlock()
	if g == nil {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// Return a detached, already-encoded snapshot so callers can marshal it
	// without racing a concurrent in-place state mutation.
	return json.RawMessage(mustJSON(g.state)), true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
