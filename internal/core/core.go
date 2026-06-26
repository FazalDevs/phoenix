// Package core holds the shared domain types and plugin interfaces that every
// internal subsystem depends on. It is a leaf package (imports nothing internal)
// so subsystems can depend on it without import cycles. The public SDK package
// pkg/phoenix re-exports these as type aliases, so developers see phoenix.Event
// while internals see core.Event — the same type.
package core

import (
	"context"
	"encoding/json"
	"time"
)

// Event is the atomic unit of Phoenix. Everything is an event. The full state of
// any match is the ordered fold of its events through a Reducer. Events are
// append-only and immutable once stored — the basis of replay and server
// authority.
type Event struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	RoomID    string          `json:"room_id"`
	PlayerID  string          `json:"player_id"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
	Version   int             `json:"version"`
}

// NewPayload builds a Payload from any value.
func NewPayload(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// Player is the actor in a game.
type Player struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	RoomID      string `json:"room_id"`
}

// Room is a session that players join.
type Room struct {
	ID         string    `json:"id"`
	OwnerID    string    `json:"owner_id"`
	GameType   string    `json:"game_type"`
	Status     string    `json:"status"`
	MaxPlayers int       `json:"max_players"`
	IsPrivate  bool      `json:"is_private"`
	InviteCode string    `json:"invite_code,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Reducer folds an event into prior state to produce new state (Redux-like).
type Reducer func(state any, e Event) (newState any, err error)

// DerivedEvent is a server-authored event emitted as a consequence of applying
// another event — e.g. a move that delivers checkmate derives a MatchEnded.
// Derived events flow through the same persist -> publish pipeline, so consumers
// (leaderboard, etc.) react to meaningful domain events rather than raw inputs.
type DerivedEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Deriver inspects a state transition and returns any domain events to emit.
type Deriver func(prevState, nextState any, cause Event) []DerivedEvent

// EventStore is the durable, append-only system of record for events. It is the
// write side only — distribution to consumers is the Bus's job (see Publisher).
type EventStore interface {
	Append(ctx context.Context, e *Event) error
	Load(ctx context.Context, roomID string, fromSeq, toSeq int64) ([]Event, error)
}

// Publisher is the bus seam the store publishes to after a durable append, so
// "published implies persisted". The in-process bus and a future Kafka producer
// both satisfy this.
type Publisher interface {
	Publish(e Event)
}

// Snapshot is a materialized fold of a room's state at a given Seq. Snapshots
// bound replay/rehydration cost: instead of folding every event from Seq 1, a
// room restores the latest snapshot and folds only the events after it.
type Snapshot struct {
	RoomID string          `json:"room_id"`
	Seq    int64           `json:"seq"`
	State  json.RawMessage `json:"state"`
}

// SnapshotStore persists and retrieves the latest snapshot per room.
type SnapshotStore interface {
	Save(ctx context.Context, s Snapshot) error
	Latest(ctx context.Context, roomID string) (Snapshot, bool, error)
}

// PresenceStore tracks ephemeral player status (Redis-backed in production).
type PresenceStore interface {
	Set(ctx context.Context, playerID string, status PresenceStatus) error
	Get(ctx context.Context, playerID string) (PresenceStatus, error)
	OnlineCount(ctx context.Context) (int, error)
}

type PresenceStatus string

const (
	StatusOnline       PresenceStatus = "online"
	StatusOffline      PresenceStatus = "offline"
	StatusIdle         PresenceStatus = "idle"
	StatusPlaying      PresenceStatus = "playing"
	StatusTyping       PresenceStatus = "typing"
	StatusDisconnected PresenceStatus = "disconnected"
)

// Matchmaker assigns players to rooms.
type Matchmaker interface {
	FindMatch(ctx context.Context, p MatchRequest) (roomID string, err error)
}

// MatchRequest is the input to matchmaking.
type MatchRequest struct {
	PlayerID string
	Rank     int
	PingMS   int
	Region   string
	GameMode string
}
