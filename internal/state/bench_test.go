package state

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/store"
)

// discardConn is a no-op connection for benchmarking the pipeline without I/O.
type discardConn struct{ connID, playerID, display, roomID string }

func (c *discardConn) ConnID() string      { return c.connID }
func (c *discardConn) PlayerID() string    { return c.playerID }
func (c *discardConn) DisplayName() string { return c.display }
func (c *discardConn) RoomID() string      { return c.roomID }
func (c *discardConn) Send([]byte)         {}

// BenchmarkMovePipeline measures the full per-event hot path with the in-memory
// store: JSON parse -> validate (reducer) -> append (assign seq) -> fold ->
// marshal broadcast. This is the engine's CPU throughput per event, independent
// of the database and network.
func BenchmarkMovePipeline(b *testing.B) {
	es := store.NewMemory(nil)
	hub := NewHub(es, map[string]Handlers{"test": {
		Reducers:  map[string]core.Reducer{"inc": counterReducer},
		InitState: func() any { return map[string]any{"count": 0} },
	}})
	ctx := context.Background()
	c := &discardConn{connID: "c1", playerID: "p1", display: "Bench", roomID: "room1"}
	_ = hub.Join(ctx, c, "test", false)
	raw := []byte(`{"type":"inc","payload":{}}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.HandleMessage(ctx, c, raw)
	}
}

// BenchmarkRehydrate measures the cost of rebuilding a room's state — what
// happens on reconnect or crash recovery — comparing:
//   - fullReplay: fold the ENTIRE event log from Seq 1            (O(history))
//   - snapshot:   restore the latest snapshot + fold only the tail (O(tail))
//
// The snapshot path stays roughly constant as a match grows; full replay scales
// linearly with match length. Run: go test -bench=BenchmarkRehydrate ./internal/state/
func BenchmarkRehydrate(b *testing.B) {
	ctx := context.Background()
	const tail = 50

	reducers := map[string]core.Reducer{"inc": counterReducer}
	initState := func() any { return map[string]any{"count": 0} }
	restore := func(raw []byte) any {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}

	for _, total := range []int{1000, 5000} {
		es := store.NewMemory(nil)
		for i := 0; i < total; i++ {
			_ = es.Append(ctx, &core.Event{Type: "inc", RoomID: "r"})
		}
		snaps := store.NewMemorySnapshots()
		snapState, _ := json.Marshal(map[string]any{"count": total - tail})
		_ = snaps.Save(ctx, core.Snapshot{RoomID: "r", Seq: int64(total - tail), State: snapState})

		b.Run(fmt.Sprintf("fullReplay/%devents", total), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				h := NewHub(es, map[string]Handlers{"g": {Reducers: reducers, InitState: initState}})
				_ = h.getOrCreate(ctx, "r", "g") // folds all `total` events
			}
		})
		b.Run(fmt.Sprintf("snapshot/%devents", total), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				h := NewHub(es, map[string]Handlers{"g": {Reducers: reducers, InitState: initState,
					Snapshots: snaps, Restore: restore}})
				_ = h.getOrCreate(ctx, "r", "g") // restores snapshot + folds `tail`
			}
		})
	}
}

// representativeState is a chess-like game state (~mid-game) so the JSON payload
// is realistic, not trivially tiny.
func representativeState() any {
	return map[string]any{
		"fen":    "r1bqkb1r/pppp1ppp/2n2n2/4p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4",
		"turn":   "w",
		"status": "active",
		"players": map[string]any{"w": "a1b2c3d4-0000-4000-8000-000000000001",
			"b": "a1b2c3d4-0000-4000-8000-000000000002"},
		"history": []string{"e4", "e5", "Nf3", "Nc6", "Bc4", "Nf6", "d3", "Bc5",
			"O-O", "d6", "Re1", "O-O", "h3", "h6", "Nc3", "a6"},
	}
}

// BenchmarkBroadcast compares two ways to push a state delta to every player in
// a room:
//   - naive:     serialize the payload once PER connection (what a first cut does)
//   - optimized: serialize once, reuse the bytes for every connection (the hub)
//
// The optimized path is what internal/state/hub.go does. The win scales with
// room size. Run with: go test -bench=BenchmarkBroadcast -benchmem ./internal/state/
func BenchmarkBroadcast(b *testing.B) {
	state := representativeState()
	e := &core.Event{ID: "x", Seq: 17, Type: "move", RoomID: "room1", PlayerID: "p1"}

	for _, k := range []int{2, 8, 20} {
		conns := make([]Conn, k)
		for i := range conns {
			conns[i] = &discardConn{connID: fmt.Sprintf("c%d", i), roomID: "room1"}
		}

		b.Run(fmt.Sprintf("naive/%dplayers", k), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, c := range conns { // serialize once per recipient
					msg := mustJSON(outbound{Type: "event", RoomID: "room1", Event: e, State: state})
					c.Send(msg)
				}
			}
		})

		b.Run(fmt.Sprintf("optimized/%dplayers", k), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				msg := mustJSON(outbound{Type: "event", RoomID: "room1", Event: e, State: state})
				for _, c := range conns { // reuse the bytes for everyone
					c.Send(msg)
				}
			}
		})
	}
}
