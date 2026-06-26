package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/fazal/phoenix/internal/core"
	"github.com/google/uuid"
)

// Memory is an in-memory EventStore for tests and offline demos. It implements
// the exact same interface as Postgres — proof that the engine is decoupled
// from storage. Not durable; data is lost on restart.
type Memory struct {
	mu     sync.Mutex
	events map[string][]core.Event // roomID -> events (Seq order)
	pub    core.Publisher
}

var _ core.EventStore = (*Memory)(nil)

// NewMemory builds an in-memory store. pub may be nil (no publishing).
func NewMemory(pub core.Publisher) *Memory {
	return &Memory{events: make(map[string][]core.Event), pub: pub}
}

func (m *Memory) Append(ctx context.Context, e *core.Event) error {
	m.mu.Lock()
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Version == 0 {
		e.Version = 1
	}
	e.Seq = int64(len(m.events[e.RoomID])) + 1
	m.events[e.RoomID] = append(m.events[e.RoomID], *e)
	m.mu.Unlock()

	if m.pub != nil {
		m.pub.Publish(*e)
	}
	return nil
}

func (m *Memory) Load(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]core.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []core.Event
	for _, e := range m.events[roomID] {
		if e.Seq >= fromSeq && (toSeq <= 0 || e.Seq <= toSeq) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
