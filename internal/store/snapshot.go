package store

import (
	"context"
	"sync"

	"github.com/fazal/phoenix/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemorySnapshots is an in-memory SnapshotStore for tests/benchmarks.
type MemorySnapshots struct {
	mu   sync.Mutex
	snap map[string]core.Snapshot // roomID -> latest snapshot
}

var _ core.SnapshotStore = (*MemorySnapshots)(nil)

func NewMemorySnapshots() *MemorySnapshots {
	return &MemorySnapshots{snap: make(map[string]core.Snapshot)}
}

func (m *MemorySnapshots) Save(_ context.Context, s core.Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.snap[s.RoomID]; !ok || s.Seq > cur.Seq {
		m.snap[s.RoomID] = s
	}
	return nil
}

func (m *MemorySnapshots) Latest(_ context.Context, roomID string) (core.Snapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.snap[roomID]
	return s, ok, nil
}

// PostgresSnapshots stores the latest snapshot per room in Postgres.
type PostgresSnapshots struct{ pool *pgxpool.Pool }

var _ core.SnapshotStore = (*PostgresSnapshots)(nil)

func NewPostgresSnapshots(pool *pgxpool.Pool) *PostgresSnapshots {
	return &PostgresSnapshots{pool: pool}
}

// EnsureSchema creates the snapshots table if missing.
func (p *PostgresSnapshots) EnsureSchema(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS snapshots (
			room_id    UUID PRIMARY KEY,
			seq        BIGINT NOT NULL,
			state      JSONB  NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	return err
}

// Save upserts the snapshot, keeping the highest Seq per room.
func (p *PostgresSnapshots) Save(ctx context.Context, s core.Snapshot) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO snapshots (room_id, seq, state) VALUES ($1,$2,$3)
		ON CONFLICT (room_id) DO UPDATE SET seq = EXCLUDED.seq, state = EXCLUDED.state, created_at = now()
		WHERE EXCLUDED.seq > snapshots.seq`,
		s.RoomID, s.Seq, s.State)
	return err
}

func (p *PostgresSnapshots) Latest(ctx context.Context, roomID string) (core.Snapshot, bool, error) {
	var s core.Snapshot
	err := p.pool.QueryRow(ctx,
		`SELECT room_id, seq, state FROM snapshots WHERE room_id=$1`, roomID,
	).Scan(&s.RoomID, &s.Seq, &s.State)
	if err == pgx.ErrNoRows {
		return core.Snapshot{}, false, nil
	}
	if err != nil {
		return core.Snapshot{}, false, err
	}
	return s, true, nil
}
