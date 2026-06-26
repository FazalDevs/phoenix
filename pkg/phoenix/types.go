package phoenix

import (
	"encoding/json"

	"github.com/fazal/phoenix/internal/core"
)

// The public SDK surface re-exports the core domain types as aliases, so game
// developers import a single package (phoenix) while the engine internals share
// the exact same underlying types from internal/core.

type (
	// Event is the atomic unit of Phoenix; everything is an event.
	Event = core.Event
	// Player is the actor in a game, passed to OnJoin/OnLeave.
	Player = core.Player
	// Room is a session players join.
	Room = core.Room
	// Reducer folds an event into prior state to produce new state.
	Reducer = core.Reducer
	// EventStore is the pluggable append-only store interface.
	EventStore = core.EventStore
	// PresenceStore is the pluggable presence interface.
	PresenceStore = core.PresenceStore
	// PresenceStatus enumerates player presence states.
	PresenceStatus = core.PresenceStatus
	// Matchmaker is the pluggable matchmaking interface.
	Matchmaker = core.Matchmaker
	// MatchRequest is the input to matchmaking.
	MatchRequest = core.MatchRequest
	// DerivedEvent is a server-authored domain event emitted from a transition.
	DerivedEvent = core.DerivedEvent
	// Deriver maps a state transition to domain events to emit.
	Deriver = core.Deriver
)

// NewPayload builds an event Payload from any value.
func NewPayload(v any) (json.RawMessage, error) {
	return core.NewPayload(v)
}

// Presence status constants re-exported for SDK users.
const (
	StatusOnline       = core.StatusOnline
	StatusOffline      = core.StatusOffline
	StatusIdle         = core.StatusIdle
	StatusPlaying      = core.StatusPlaying
	StatusTyping       = core.StatusTyping
	StatusDisconnected = core.StatusDisconnected
)
