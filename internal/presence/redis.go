// Package presence is a Redis-backed projection of player presence. It is a bus
// consumer: it reacts to PlayerJoined/PlayerLeft events and maintains live
// online status in Redis, shared across nodes. Presence is ephemeral and
// eventually consistent — exactly the kind of derived read model that belongs
// off the synchronous game path.
package presence

import (
	"context"

	"github.com/fazal/phoenix/internal/core"
	redis "github.com/redis/go-redis/v9"
)

const (
	onlineSet = "presence:online"
	statusKey = "presence:status:"
)

type Redis struct {
	rdb *redis.Client
}

var _ core.PresenceStore = (*Redis)(nil)

// NewRedis connects to Redis from a redis:// URL.
func NewRedis(ctx context.Context, url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opt)
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return &Redis{rdb: rdb}, nil
}

func (p *Redis) Close() error { return p.rdb.Close() }

// Set records a player's status and maintains the online set.
func (p *Redis) Set(ctx context.Context, playerID string, status core.PresenceStatus) error {
	pipe := p.rdb.TxPipeline()
	pipe.Set(ctx, statusKey+playerID, string(status), 0)
	if status == core.StatusOffline || status == core.StatusDisconnected {
		pipe.SRem(ctx, onlineSet, playerID)
	} else {
		pipe.SAdd(ctx, onlineSet, playerID)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (p *Redis) Get(ctx context.Context, playerID string) (core.PresenceStatus, error) {
	v, err := p.rdb.Get(ctx, statusKey+playerID).Result()
	if err == redis.Nil {
		return core.StatusOffline, nil
	}
	if err != nil {
		return "", err
	}
	return core.PresenceStatus(v), nil
}

func (p *Redis) OnlineCount(ctx context.Context) (int, error) {
	n, err := p.rdb.SCard(ctx, onlineSet).Result()
	return int(n), err
}

// Handle is the bus subscriber: it folds presence-relevant events into Redis.
func (p *Redis) Handle(e core.Event) {
	ctx := context.Background()
	switch e.Type {
	case "PlayerJoined":
		_ = p.Set(ctx, e.PlayerID, core.StatusPlaying)
	case "PlayerLeft":
		_ = p.Set(ctx, e.PlayerID, core.StatusOffline)
	}
}
