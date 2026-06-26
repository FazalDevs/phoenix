package state

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/store"
)

// fakeConn captures messages the hub sends, for assertions.
type fakeConn struct {
	connID, playerID, display, roomID string
	mu                                sync.Mutex
	msgs                              [][]byte
}

func (c *fakeConn) ConnID() string      { return c.connID }
func (c *fakeConn) PlayerID() string    { return c.playerID }
func (c *fakeConn) DisplayName() string { return c.display }
func (c *fakeConn) RoomID() string      { return c.roomID }
func (c *fakeConn) Send(m []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
}
func (c *fakeConn) last() outbound {
	c.mu.Lock()
	defer c.mu.Unlock()
	var o outbound
	if len(c.msgs) > 0 {
		_ = json.Unmarshal(c.msgs[len(c.msgs)-1], &o)
	}
	return o
}

// counterReducer increments state["count"] on each "inc" event.
func counterReducer(s any, _ core.Event) (any, error) {
	st, _ := s.(map[string]any)
	if st == nil {
		st = map[string]any{}
	}
	n, _ := st["count"].(int)
	st["count"] = n + 1
	return st, nil
}

func TestEventReducerLoop(t *testing.T) {
	es := store.NewMemory(nil)
	hub := NewHub(es, Handlers{
		Reducers:  map[string]core.Reducer{"inc": counterReducer},
		InitState: func() any { return map[string]any{"count": 0} },
	})
	ctx := context.Background()
	c := &fakeConn{connID: "c1", playerID: "p1", display: "Alice", roomID: "room1"}

	if err := hub.Join(ctx, c, false); err != nil {
		t.Fatalf("join: %v", err)
	}

	// Three intents -> three events -> count == 3.
	for i := 0; i < 3; i++ {
		hub.HandleMessage(ctx, c, []byte(`{"type":"inc","payload":{}}`))
	}

	out := c.last()
	if out.Type != "event" {
		t.Fatalf("expected last msg type 'event', got %q", out.Type)
	}
	st, _ := out.State.(map[string]any)
	// JSON round-trips numbers as float64.
	if got := st["count"]; got != float64(3) {
		t.Fatalf("expected count=3, got %v", got)
	}

	// The append-only log must hold: PlayerJoined + 3 inc = 4 events.
	events, err := es.Load(ctx, "room1", 1, 0)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events in log, got %d", len(events))
	}
	if events[0].Type != "PlayerJoined" {
		t.Fatalf("expected first event PlayerJoined, got %q", events[0].Type)
	}

	// Reconnect rebuild: a brand-new hub on the same store must reconstruct
	// count==3 purely by replaying the log (crash-recovery property).
	hub2 := NewHub(es, Handlers{
		Reducers:  map[string]core.Reducer{"inc": counterReducer, "PlayerJoined": func(s any, _ core.Event) (any, error) { return s, nil }},
		InitState: func() any { return map[string]any{"count": 0} },
	})
	g := hub2.getOrCreate(ctx, "room1")
	g.mu.Lock()
	rebuilt, _ := g.state.(map[string]any)
	g.mu.Unlock()
	if rebuilt["count"] != 3 {
		t.Fatalf("rebuild from log: expected count=3, got %v", rebuilt["count"])
	}
}
