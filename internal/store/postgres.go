// Package store provides EventStore implementations. Postgres is the default
// durable, queryable system of record; Memory is for tests and demos. After a
// durable append, the store publishes the event to the bus (core.Publisher) so
// downstream consumers react — "published implies persisted".
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/fazal/phoenix/internal/core"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the append-only EventStore backed by the events table.
type Postgres struct {
	pool *pgxpool.Pool
	pub  core.Publisher // bus to publish appended events to (may be nil)
}

// compile-time assertion that Postgres satisfies the interface.
var _ core.EventStore = (*Postgres)(nil)

// NewPostgres connects to the database and returns a ready EventStore.
func NewPostgres(ctx context.Context, dsn string, pub core.Publisher) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool, pub: pub}, nil
}

// NewFromPool wraps an existing pool (so the engine shares one pool across the
// event store, auth, and room services) and a bus to publish to.
func NewFromPool(pool *pgxpool.Pool, pub core.Publisher) *Postgres {
	return &Postgres{pool: pool, pub: pub}
}

func (p *Postgres) Close() { p.pool.Close() }

// Append assigns a per-room monotonic Seq, durably writes the event, then
// publishes it to the bus. A transaction-scoped advisory lock keyed on the room
// serializes concurrent appends to the same room so Seq has no gaps or
// duplicates; different rooms append fully in parallel.
func (p *Postgres) Append(ctx context.Context, e *core.Event) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Version == 0 {
		e.Version = 1
	}

	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Serialize appends per room (hashtext maps the room id to a lock key).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, e.RoomID); err != nil {
		return fmt.Errorf("lock room: %w", err)
	}

	var next int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(seq),0)+1 FROM events WHERE room_id=$1`, e.RoomID,
	).Scan(&next); err != nil {
		return fmt.Errorf("next seq: %w", err)
	}
	e.Seq = next

	var playerID any
	if e.PlayerID != "" {
		playerID = e.PlayerID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO events (id, room_id, seq, type, player_id, payload, version, timestamp)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		e.ID, e.RoomID, e.Seq, e.Type, playerID, e.Payload, e.Version, e.Timestamp,
	); err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Durable -> publish to the bus for async consumers (presence, leaderboard,
	// dashboard stream). Never blocks the caller.
	if p.pub != nil {
		p.pub.Publish(*e)
	}
	return nil
}

// Load returns events for a room with Seq in [fromSeq, toSeq] ordered by Seq.
// toSeq <= 0 means "up to the latest".
func (p *Postgres) Load(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]core.Event, error) {
	q := `SELECT id, room_id, seq, type, COALESCE(player_id::text,''), payload, version, timestamp
	      FROM events WHERE room_id=$1 AND seq>=$2`
	args := []any{roomID, fromSeq}
	if toSeq > 0 {
		q += ` AND seq<=$3`
		args = append(args, toSeq)
	}
	q += ` ORDER BY seq ASC`

	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Event
	for rows.Next() {
		var e core.Event
		if err := rows.Scan(&e.ID, &e.RoomID, &e.Seq, &e.Type, &e.PlayerID, &e.Payload, &e.Version, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
