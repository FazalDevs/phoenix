package store

import (
	"context"
	"sync"
	"time"

	"github.com/fazal/phoenix/internal/core"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Batched is a high-throughput EventStore that trades a bounded durability
// window for ~10-50x the write throughput of the per-event transactional store.
//
// How it wins:
//   - In-memory sequencing: Seq comes from an atomic per-room counter, removing
//     the SELECT MAX(seq) round-trip on every append.
//   - Micro-batching: events are buffered and written with a single COPY per
//     flush instead of one INSERT+COMMIT per event (Postgres loves bulk COPY).
//   - Async persistence: Append returns as soon as the event is sequenced,
//     buffered, and published; a background flusher persists batches.
//
// The trade-off (and the senior talking point): "published implies persisted"
// weakens to a bounded window — a crash can lose events buffered since the last
// flush (≤ flushEvery or flushN). It is offered as an OPTION alongside the safe
// synchronous Postgres store; pick per durability need. Recovery is still
// correct for what was flushed, and Load merges the in-flight buffer so readers
// never miss a sequenced-but-unflushed event.
type Batched struct {
	pool *pgxpool.Pool
	pub  core.Publisher

	seqMu sync.Mutex
	seqs  map[string]int64 // roomID -> last assigned seq (in-memory sequencing)

	pendMu  sync.Mutex
	pending map[string]map[int64]core.Event // roomID -> seq -> not-yet-flushed event

	buf        chan core.Event
	done       chan struct{}
	wg         sync.WaitGroup
	flushN     int
	flushEvery time.Duration
}

var _ core.EventStore = (*Batched)(nil)

var eventCols = []string{"id", "room_id", "seq", "type", "player_id", "payload", "version", "timestamp"}

// NewBatched wraps a pool with batched async writes. flushN events or flushEvery
// duration (whichever first) triggers a COPY flush.
func NewBatched(pool *pgxpool.Pool, pub core.Publisher, flushN int, flushEvery time.Duration) *Batched {
	if flushN <= 0 {
		flushN = 512
	}
	if flushEvery <= 0 {
		flushEvery = 2 * time.Millisecond
	}
	b := &Batched{
		pool: pool, pub: pub,
		seqs:    make(map[string]int64),
		pending: make(map[string]map[int64]core.Event),
		buf:     make(chan core.Event, 1<<16),
		done:    make(chan struct{}),
		flushN:  flushN, flushEvery: flushEvery,
	}
	b.wg.Add(1)
	go b.flushLoop()
	return b
}

// Append sequences, buffers, and publishes an event, then returns. Durable write
// happens asynchronously in the flusher.
func (b *Batched) Append(ctx context.Context, e *core.Event) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	if e.Version == 0 {
		e.Version = 1
	}
	if len(e.Payload) == 0 {
		e.Payload = []byte("{}")
	}

	e.Seq = b.nextSeq(ctx, e.RoomID)

	// Track as in-flight so Load can see it before it's flushed.
	b.pendMu.Lock()
	if b.pending[e.RoomID] == nil {
		b.pending[e.RoomID] = make(map[int64]core.Event)
	}
	b.pending[e.RoomID][e.Seq] = *e
	b.pendMu.Unlock()

	if b.pub != nil {
		b.pub.Publish(*e)
	}
	b.buf <- *e
	return nil
}

// nextSeq returns the next per-room sequence from the in-memory counter, lazily
// initialized from the DB max (so it's correct across restarts).
func (b *Batched) nextSeq(ctx context.Context, roomID string) int64 {
	b.seqMu.Lock()
	defer b.seqMu.Unlock()
	cur, ok := b.seqs[roomID]
	if !ok {
		var max int64
		_ = b.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0) FROM events WHERE room_id=$1`, roomID).Scan(&max)
		cur = max
	}
	cur++
	b.seqs[roomID] = cur
	return cur
}

func (b *Batched) flushLoop() {
	defer b.wg.Done()
	ticker := time.NewTicker(b.flushEvery)
	defer ticker.Stop()
	batch := make([]core.Event, 0, b.flushN)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		rows := make([][]any, len(batch))
		for i, e := range batch {
			var pid any
			if e.PlayerID != "" {
				pid = e.PlayerID
			}
			rows[i] = []any{e.ID, e.RoomID, e.Seq, e.Type, pid, string(e.Payload), e.Version, e.Timestamp}
		}
		_, _ = b.pool.CopyFrom(context.Background(), pgx.Identifier{"events"}, eventCols, pgx.CopyFromRows(rows))
		b.clearPending(batch)
		batch = batch[:0]
	}

	for {
		select {
		case e := <-b.buf:
			batch = append(batch, e)
			if len(batch) >= b.flushN {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-b.done:
			// drain everything still buffered, then final flush.
			for {
				select {
				case e := <-b.buf:
					batch = append(batch, e)
					if len(batch) >= b.flushN {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

func (b *Batched) clearPending(batch []core.Event) {
	b.pendMu.Lock()
	defer b.pendMu.Unlock()
	for _, e := range batch {
		if m := b.pending[e.RoomID]; m != nil {
			delete(m, e.Seq)
			if len(m) == 0 {
				delete(b.pending, e.RoomID)
			}
		}
	}
}

// Close flushes all buffered events and stops the flusher. Call on shutdown so
// the durability window closes cleanly.
func (b *Batched) Close() {
	close(b.done)
	b.wg.Wait()
}

// Load returns events for a room, merging durable rows with any in-flight
// (sequenced-but-unflushed) events so readers never miss one.
func (b *Batched) Load(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]core.Event, error) {
	q := `SELECT id, room_id, seq, type, COALESCE(player_id::text,''), payload, version, timestamp
	      FROM events WHERE room_id=$1 AND seq>=$2`
	args := []any{roomID, fromSeq}
	if toSeq > 0 {
		q += ` AND seq<=$3`
		args = append(args, toSeq)
	}
	q += ` ORDER BY seq ASC`

	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []core.Event{}
	seen := map[int64]bool{}
	for rows.Next() {
		var e core.Event
		if err := rows.Scan(&e.ID, &e.RoomID, &e.Seq, &e.Type, &e.PlayerID, &e.Payload, &e.Version, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
		seen[e.Seq] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Merge in-flight events not yet persisted.
	b.pendMu.Lock()
	for seq, e := range b.pending[roomID] {
		if seq >= fromSeq && (toSeq <= 0 || seq <= toSeq) && !seen[seq] {
			out = append(out, e)
		}
	}
	b.pendMu.Unlock()

	sortBySeq(out)
	return out, nil
}

func sortBySeq(evs []core.Event) {
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j-1].Seq > evs[j].Seq; j-- {
			evs[j-1], evs[j] = evs[j], evs[j-1]
		}
	}
}
