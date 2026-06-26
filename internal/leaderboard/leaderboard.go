// Package leaderboard is a Postgres-backed read model (CQRS projection) built by
// folding MatchEnded events from the log. It is a bus consumer: when a match
// ends it recomputes standings. Because the projection is derived purely from
// the event log, it is fully rebuildable — on boot it recomputes from scratch,
// so it is always exactly the fold of history and never drifts. (Recompute on
// every match end is the simple, idempotent baseline; incremental updates are
// the obvious optimization.)
package leaderboard

import (
	"context"
	"encoding/json"

	"github.com/fazal/phoenix/internal/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// EnsureSchema creates the projection table if needed (so it works even when the
// initial migration didn't run against an already-initialized database).
func (s *Service) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS leaderboard (
			player_id UUID PRIMARY KEY,
			wins   INT NOT NULL DEFAULT 0,
			losses INT NOT NULL DEFAULT 0,
			draws  INT NOT NULL DEFAULT 0
		)`)
	return err
}

// matchEndedPayload is the body of a MatchEnded derived event.
type matchEndedPayload struct {
	Winner  string `json:"winner"` // "w" | "b" | "" (draw)
	Players struct {
		W string `json:"w"`
		B string `json:"b"`
	} `json:"players"`
	Reason string `json:"reason"`
}

// Handle is the bus subscriber. On a MatchEnded it recomputes the projection.
func (s *Service) Handle(e core.Event) {
	if e.Type != "MatchEnded" {
		return
	}
	_ = s.RebuildFromLog(context.Background())
}

// RebuildFromLog recomputes the entire leaderboard by folding every MatchEnded
// event in the log. This is the projection's source of truth and is idempotent.
func (s *Service) RebuildFromLog(ctx context.Context) error {
	rows, err := s.pool.Query(ctx,
		`SELECT payload FROM events WHERE type='MatchEnded' ORDER BY timestamp ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rec struct{ wins, losses, draws int }
	standings := map[string]*rec{}
	get := func(id string) *rec {
		if id == "" {
			return nil
		}
		if standings[id] == nil {
			standings[id] = &rec{}
		}
		return standings[id]
	}

	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var p matchEndedPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		w, b := get(p.Players.W), get(p.Players.B)
		switch p.Winner {
		case "w":
			if w != nil {
				w.wins++
			}
			if b != nil {
				b.losses++
			}
		case "b":
			if b != nil {
				b.wins++
			}
			if w != nil {
				w.losses++
			}
		default: // draw / stalemate
			if w != nil {
				w.draws++
			}
			if b != nil {
				b.draws++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Replace the projection atomically.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `TRUNCATE leaderboard`); err != nil {
		return err
	}
	for id, r := range standings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO leaderboard (player_id, wins, losses, draws) VALUES ($1,$2,$3,$4)`,
			id, r.wins, r.losses, r.draws); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Standing is one row of the leaderboard, enriched with the player's name.
type Standing struct {
	PlayerID    string `json:"player_id"`
	DisplayName string `json:"display_name"`
	Wins        int    `json:"wins"`
	Losses      int    `json:"losses"`
	Draws       int    `json:"draws"`
}

// Top returns the highest-ranked players (by wins, then fewest losses).
func (s *Service) Top(ctx context.Context, n int) ([]Standing, error) {
	if n <= 0 {
		n = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT l.player_id, COALESCE(u.display_name,'?'), l.wins, l.losses, l.draws
		FROM leaderboard l
		LEFT JOIN users u ON u.id = l.player_id
		ORDER BY l.wins DESC, l.losses ASC
		LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Standing{}
	for rows.Next() {
		var st Standing
		if err := rows.Scan(&st.PlayerID, &st.DisplayName, &st.Wins, &st.Losses, &st.Draws); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}
