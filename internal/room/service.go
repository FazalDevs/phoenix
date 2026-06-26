// Package room manages room lifecycle: create, list, fetch, close. Durable room
// metadata lives in Postgres; live membership and game state are owned by the
// state hub and reconstructed from the event log, keeping this service thin.
package room

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"

	"github.com/fazal/phoenix/internal/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("room not found")

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

type CreateParams struct {
	OwnerID    string
	GameType   string
	MaxPlayers int
	IsPrivate  bool
}

// Create inserts a new room. Private rooms get a short invite code.
func (s *Service) Create(ctx context.Context, p CreateParams) (*core.Room, error) {
	if p.GameType == "" {
		return nil, errors.New("game_type required")
	}
	if p.MaxPlayers <= 0 {
		p.MaxPlayers = 8
	}
	var invite any
	if p.IsPrivate {
		invite = inviteCode()
	}
	var owner any
	if p.OwnerID != "" {
		owner = p.OwnerID
	}
	r := &core.Room{}
	var code *string
	err := s.pool.QueryRow(ctx,
		`INSERT INTO rooms (owner_id, game_type, max_players, is_private, invite_code)
		 VALUES ($1,$2,$3,$4,$5)
		 RETURNING id, COALESCE(owner_id::text,''), game_type, status, max_players, is_private, invite_code, created_at`,
		owner, p.GameType, p.MaxPlayers, p.IsPrivate, invite,
	).Scan(&r.ID, &r.OwnerID, &r.GameType, &r.Status, &r.MaxPlayers, &r.IsPrivate, &code, &r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}
	if code != nil {
		r.InviteCode = *code
	}
	return r, nil
}

func (s *Service) Get(ctx context.Context, id string) (*core.Room, error) {
	r := &core.Room{}
	var code *string
	err := s.pool.QueryRow(ctx,
		`SELECT id, COALESCE(owner_id::text,''), game_type, status, max_players, is_private, invite_code, created_at
		 FROM rooms WHERE id=$1`, id,
	).Scan(&r.ID, &r.OwnerID, &r.GameType, &r.Status, &r.MaxPlayers, &r.IsPrivate, &code, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if code != nil {
		r.InviteCode = *code
	}
	return r, nil
}

// List returns rooms, newest first. includeClosed=false hides closed rooms.
func (s *Service) List(ctx context.Context, includeClosed bool) ([]core.Room, error) {
	q := `SELECT id, COALESCE(owner_id::text,''), game_type, status, max_players, is_private, invite_code, created_at
	      FROM rooms`
	if !includeClosed {
		q += ` WHERE status <> 'closed'`
	}
	q += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Room
	for rows.Next() {
		var r core.Room
		var code *string
		if err := rows.Scan(&r.ID, &r.OwnerID, &r.GameType, &r.Status, &r.MaxPlayers, &r.IsPrivate, &code, &r.CreatedAt); err != nil {
			return nil, err
		}
		if code != nil {
			r.InviteCode = *code
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetStatus updates a room's lifecycle status (open/playing/closed).
func (s *Service) SetStatus(ctx context.Context, id, status string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE rooms SET status=$2 WHERE id=$1`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Close terminates a room (admin terminate / owner destroy).
func (s *Service) Close(ctx context.Context, id string) error {
	return s.SetStatus(ctx, id, "closed")
}

func inviteCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}
