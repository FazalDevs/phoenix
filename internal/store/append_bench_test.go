package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/fazal/phoenix/internal/core"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BenchmarkAppendThroughput compares durable write throughput of the per-event
// transactional store vs the batched async store, against a real Postgres.
//
// Run (sets the DB and a fixed iteration count so row growth is bounded):
//
//	$env:DATABASE_URL="postgres://phoenix:phoenix@127.0.0.1:55432/phoenix?sslmode=disable"
//	go test -run=^$ -bench=BenchmarkAppendThroughput -benchtime=20000x ./internal/store/
func BenchmarkAppendThroughput(b *testing.B) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		b.Skip("set DATABASE_URL to run the Postgres append benchmark")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		b.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	cleanup := func(room string) {
		_, _ = pool.Exec(ctx, `DELETE FROM events WHERE room_id=$1`, room)
	}

	b.Run("sync_per_event_txn", func(b *testing.B) {
		s := NewFromPool(pool, nil)
		room := uuid.NewString()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = s.Append(ctx, &core.Event{RoomID: room, Type: "bench", Payload: []byte(`{}`)})
		}
		b.StopTimer()
		cleanup(room)
	})

	b.Run("batched_async_copy", func(b *testing.B) {
		s := NewBatched(pool, nil, 512, 2*time.Millisecond)
		room := uuid.NewString()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = s.Append(ctx, &core.Event{RoomID: room, Type: "bench", Payload: []byte(`{}`)})
		}
		s.Close() // flush remaining so the timed window includes full durability
		b.StopTimer()
		cleanup(room)
	})
}
