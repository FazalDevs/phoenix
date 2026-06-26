package eventbus

import (
	"context"
	"encoding/json"

	"github.com/fazal/phoenix/internal/core"
	redis "github.com/redis/go-redis/v9"
)

// RedisBus is a multi-node event bus: it publishes events to a Redis channel and
// every node (including the publisher) receives them back from Redis and fans
// them out to its local subscribers. This makes consumers — presence,
// leaderboard, dashboard live-stream — work cluster-wide: a game processed on
// node A updates projections and dashboards served by node B.
//
// Local delivery goes through an embedded InProcess bus, so the Subscribe/filter
// semantics are identical to single-node. Publishing always round-trips Redis so
// every node delivers each event exactly once locally (no double-delivery on the
// origin node). Distributed consumers stay correct because the projections are
// idempotent (presence is a set; the leaderboard refolds the log); a production
// setup would add Redis consumer groups so only one node processes each event.
type RedisBus struct {
	local   *InProcess
	rdb     *redis.Client
	channel string
	cancel  context.CancelFunc
}

var _ Bus = (*RedisBus)(nil)
var _ core.Publisher = (*RedisBus)(nil)

// NewRedisBus connects to Redis and starts the cluster subscriber.
func NewRedisBus(url string) (*RedisBus, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithCancel(context.Background())
	if err := rdb.Ping(ctx).Err(); err != nil {
		cancel()
		return nil, err
	}
	b := &RedisBus{local: NewInProcess(), rdb: rdb, channel: "phoenix:events", cancel: cancel}
	go b.consume(ctx)
	return b, nil
}

// Publish encodes the event and publishes it to Redis. It does NOT deliver
// locally here — the consumer loop delivers it (along with events from other
// nodes), so every node delivers uniformly.
func (b *RedisBus) Publish(e core.Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	b.rdb.Publish(context.Background(), b.channel, data)
}

// consume reads the cluster channel and fans events out to local subscribers.
func (b *RedisBus) consume(ctx context.Context) {
	sub := b.rdb.Subscribe(ctx, b.channel)
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			_ = sub.Close()
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var e core.Event
			if err := json.Unmarshal([]byte(msg.Payload), &e); err == nil {
				b.local.Publish(e) // deliver to this node's subscribers
			}
		}
	}
}

func (b *RedisBus) Subscribe(name string, h Handler, opts ...SubOption) func() {
	return b.local.Subscribe(name, h, opts...)
}

func (b *RedisBus) Stats() Stats { return b.local.Stats() }

func (b *RedisBus) Close() {
	b.cancel()
	_ = b.rdb.Close()
}
