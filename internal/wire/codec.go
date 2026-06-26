// Package wire defines how server->client messages are encoded and what they
// contain. Two orthogonal optimizations live here:
//
//  1. Delta vs full-state: a full-state message re-sends the entire game state
//     every event (its size grows with match history); a delta message sends
//     only the event that changed, which the client applies locally (constant
//     size, independent of match length).
//  2. Binary vs JSON: MessagePack encodes the same message in a compact binary
//     form, smaller and faster to (de)serialize than JSON.
//
// Both are pluggable via Codec, so the transport can be chosen per deployment
// without touching game logic.
package wire

import (
	"encoding/json"

	"github.com/fazal/phoenix/internal/core"
	"github.com/vmihailenco/msgpack/v5"
)

// FullStateMsg re-sends the whole reduced state on every event (current default).
type FullStateMsg struct {
	Type   string      `json:"type" msgpack:"type"`
	RoomID string      `json:"room_id" msgpack:"room_id"`
	Event  *core.Event `json:"event" msgpack:"event"`
	State  any         `json:"state" msgpack:"state"`
}

// DeltaMsg sends only the event; the client applies it to its local state.
type DeltaMsg struct {
	Type   string      `json:"type" msgpack:"type"`
	RoomID string      `json:"room_id" msgpack:"room_id"`
	Event  *core.Event `json:"event" msgpack:"event"`
}

// Codec encodes a message to bytes.
type Codec interface {
	Name() string
	Marshal(v any) ([]byte, error)
}

type JSONCodec struct{}

func (JSONCodec) Name() string                  { return "json" }
func (JSONCodec) Marshal(v any) ([]byte, error) { return json.Marshal(v) }

type MsgpackCodec struct{}

func (MsgpackCodec) Name() string                  { return "msgpack" }
func (MsgpackCodec) Marshal(v any) ([]byte, error) { return msgpack.Marshal(v) }
