package admin

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/coder/websocket"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/eventbus"
	"github.com/google/uuid"
)

// streamMsg mirrors the game socket's event message so the dashboard can reuse
// the same client-side handling: {type:"event", event, state}.
type streamMsg struct {
	Type  string `json:"type"`
	Event any    `json:"event"`
	State any    `json:"state,omitempty"`
}

// stream is a read-only WebSocket that pushes a room's events live as they are
// appended, by subscribing to the event bus filtered to this room. This is the
// dashboard's live event feed and a concrete demonstration of a bus consumer.
// No auth in Phase 1.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("id")
	if h.bus == nil {
		http.Error(w, "bus unavailable", http.StatusServiceUnavailable)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Detect client disconnect: a read error cancels the context.
	go func() {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()

	// Bridge bus -> this socket via a local channel (the bus handler must not
	// block; the writer goroutine does the I/O).
	events := make(chan core.Event, 64)
	unsub := h.bus.Subscribe("admin-stream-"+uuid.NewString(), func(e core.Event) {
		select {
		case events <- e:
		default:
		}
	}, eventbus.WithRoom(roomID))
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-events:
			state, _ := h.hub.CurrentState(roomID)
			b, _ := json.Marshal(streamMsg{Type: "event", Event: e, State: state})
			if err := ws.Write(ctx, websocket.MessageText, b); err != nil {
				return
			}
		}
	}
}
