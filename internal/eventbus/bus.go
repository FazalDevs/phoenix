// Package eventbus is the internal event bus that decouples the write path from
// derived read models. After the event store durably appends an event, it
// publishes that event to the bus; independent consumer services (presence,
// leaderboard, dashboard stream, metrics) subscribe and react asynchronously.
//
// This is the seam that makes Phoenix event-driven: services never call each
// other, they react to events. The in-process implementation here is swappable
// for Kafka/NATS in a distributed deployment without changing any consumer —
// the Bus interface and the core.Publisher contract stay identical.
//
// Delivery semantics (in-process): each subscriber has its own buffered queue
// drained by a dedicated goroutine, so a slow consumer never blocks the
// publisher or other consumers. If a subscriber's queue overflows, events are
// dropped and counted (Lag) rather than blocking the authoritative write path.
// Consumers that must not miss events rebuild from the event log on startup
// (the log is the source of truth) — see the leaderboard projection. In a
// distributed setup this maps onto consumer-group offsets + replay.
package eventbus

import (
	"sync"
	"sync/atomic"

	"github.com/fazal/phoenix/internal/core"
)

// Handler reacts to a single event. It runs on the subscriber's own goroutine,
// so it may block (e.g. do I/O) without affecting the publisher.
type Handler func(core.Event)

// Bus is publish/subscribe over events. Publish never blocks on consumers.
type Bus interface {
	Publish(e core.Event)
	Subscribe(name string, h Handler, opts ...SubOption) (unsubscribe func())
	Stats() Stats
}

// SubOption configures a subscription.
type SubOption func(*subscriber)

// WithRoom delivers only events for the given room (e.g. a dashboard watching
// one match). Empty/unset means all rooms.
func WithRoom(roomID string) SubOption {
	return func(s *subscriber) { s.roomFilter = roomID }
}

// WithBuffer sets the subscriber queue depth (default 256).
func WithBuffer(n int) SubOption {
	return func(s *subscriber) { s.bufSize = n }
}

type subscriber struct {
	name       string
	roomFilter string
	bufSize    int
	ch         chan core.Event
	dropped    atomic.Int64
	done       chan struct{}
}

// InProcess is a single-node Bus. Subsumes the previous per-room broker.
type InProcess struct {
	mu        sync.RWMutex
	subs      map[int]*subscriber
	nextID    int
	published atomic.Int64
}

func NewInProcess() *InProcess {
	return &InProcess{subs: make(map[int]*subscriber)}
}

var _ Bus = (*InProcess)(nil)
var _ core.Publisher = (*InProcess)(nil)

// Publish fans the event out to every matching subscriber without blocking.
func (b *InProcess) Publish(e core.Event) {
	b.published.Add(1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		if s.roomFilter != "" && s.roomFilter != e.RoomID {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.dropped.Add(1) // consumer is behind; never stall the writer
		}
	}
}

// Subscribe registers a consumer and starts its delivery goroutine. The
// returned func unsubscribes and stops the goroutine.
func (b *InProcess) Subscribe(name string, h Handler, opts ...SubOption) func() {
	s := &subscriber{name: name, bufSize: 256, done: make(chan struct{})}
	for _, o := range opts {
		o(s)
	}
	s.ch = make(chan core.Event, s.bufSize)

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = s
	b.mu.Unlock()

	go func() {
		for {
			select {
			case <-s.done:
				return
			case e := <-s.ch:
				h(e)
			}
		}
	}()

	return func() {
		b.mu.Lock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(s.done)
		}
		b.mu.Unlock()
	}
}

// Stats reports bus-level counters for the metrics dashboard.
type Stats struct {
	Published   int64 `json:"published"`
	Subscribers int   `json:"subscribers"`
}

func (b *InProcess) Stats() Stats {
	b.mu.RLock()
	n := len(b.subs)
	b.mu.RUnlock()
	return Stats{Published: b.published.Load(), Subscribers: n}
}
