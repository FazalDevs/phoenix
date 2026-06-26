// Package matchmaking pairs players into rooms. v1 is a simple "next open seat"
// matcher: the first player to quick-match creates a room and waits; the second
// is paired into it. It implements core.Matchmaker, so a ranked/skill-based
// matcher can replace it without touching callers (the plugin seam).
package matchmaking

import (
	"context"
	"sync"

	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/room"
)

type Service struct {
	rooms *room.Service

	mu      sync.Mutex
	waiting map[string]string // gameType -> roomID currently awaiting an opponent
}

func NewService(rooms *room.Service) *Service {
	return &Service{rooms: rooms, waiting: make(map[string]string)}
}

var _ core.Matchmaker = (*Service)(nil)

// QuickMatch returns a room for the player to join: an existing one waiting for
// an opponent, or a freshly created room that now waits. Two players calling
// this back-to-back land in the same room.
func (s *Service) QuickMatch(ctx context.Context, gameType, playerID string) (*core.Room, error) {
	if gameType == "" {
		gameType = "chess"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if rid, ok := s.waiting[gameType]; ok {
		delete(s.waiting, gameType)
		if rm, err := s.rooms.Get(ctx, rid); err == nil && rm.Status != "closed" {
			return rm, nil // pair the second player into the waiting room
		}
		// stale/closed room — fall through and create a fresh one
	}

	rm, err := s.rooms.Create(ctx, room.CreateParams{OwnerID: playerID, GameType: gameType, MaxPlayers: 2})
	if err != nil {
		return nil, err
	}
	s.waiting[gameType] = rm.ID
	return rm, nil
}

// FindMatch satisfies core.Matchmaker.
func (s *Service) FindMatch(ctx context.Context, p core.MatchRequest) (string, error) {
	rm, err := s.QuickMatch(ctx, p.GameMode, p.PlayerID)
	if err != nil {
		return "", err
	}
	return rm.ID, nil
}
