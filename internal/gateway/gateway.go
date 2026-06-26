// Package gateway terminates WebSocket connections: it authenticates the
// handshake, validates the target room, and bridges the socket to the state
// hub. It owns connection liveness (heartbeat ping/pong) and the reconnect
// grace window — a dropped player stays logically present for ReconnectWindow
// so a flaky network doesn't eject them mid-match.
package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/fazal/phoenix/internal/auth"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/room"
	"github.com/fazal/phoenix/internal/state"
	"github.com/google/uuid"
)

type Gateway struct {
	auth            *auth.Handler
	users           *auth.Service
	rooms           *room.Service
	hub             *state.Hub
	heartbeat       time.Duration
	reconnectWindow time.Duration

	mu      sync.Mutex
	pending map[string]*time.Timer // "roomID|playerID" -> pending PlayerLeft
}

func New(a *auth.Handler, users *auth.Service, rooms *room.Service, hub *state.Hub, heartbeat, reconnect time.Duration) *Gateway {
	return &Gateway{
		auth: a, users: users, rooms: rooms, hub: hub,
		heartbeat: heartbeat, reconnectWindow: reconnect,
		pending: make(map[string]*time.Timer),
	}
}

// ServeWS handles GET /ws?room=<id>&token=<jwt>.
func (g *Gateway) ServeWS(w http.ResponseWriter, r *http.Request) {
	userID, ok := g.auth.Authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	roomID := r.URL.Query().Get("room")
	if roomID == "" {
		http.Error(w, "room required", http.StatusBadRequest)
		return
	}

	rm, err := g.rooms.Get(r.Context(), roomID)
	if err != nil || rm.Status == "closed" {
		http.Error(w, "room unavailable", http.StatusNotFound)
		return
	}
	u, err := g.users.Lookup(r.Context(), userID)
	if err != nil {
		http.Error(w, "unknown user", http.StatusUnauthorized)
		return
	}
	if u.Banned {
		http.Error(w, "banned", http.StatusForbidden)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionContextTakeover,
		// The browser game client is served from a different origin (e.g. :3000)
		// than the backend (:8090). Allow it. Tighten via OriginPatterns in prod.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return // Accept already wrote the response
	}
	ws.SetReadLimit(1 << 20) // 1 MiB max message

	// Long-lived connection context, independent of the HTTP request.
	ctx, cancel := context.WithCancel(context.Background())
	c := &wsConn{
		connID: uuid.NewString(), playerID: u.ID, display: u.DisplayName,
		roomID: roomID, ws: ws, out: make(chan []byte, 256), cancel: cancel,
	}

	// Reconnect detection: if a PlayerLeft was pending for this player+room,
	// cancel it — this socket is a reconnection, not a fresh join.
	isReconnect := g.cancelPendingLeave(roomID, u.ID)

	if err := g.hub.Join(ctx, c, rm.GameType, isReconnect); err != nil {
		ws.Close(websocket.StatusInternalError, "join failed")
		cancel()
		return
	}

	go c.writePump(ctx, g.heartbeat)
	g.readPump(ctx, c) // blocks until the socket closes
}

// readPump reads client intents until the socket dies, then schedules the
// reconnect grace timer.
func (g *Gateway) readPump(ctx context.Context, c *wsConn) {
	defer c.cancel()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			break
		}
		g.hub.HandleMessage(ctx, c, data)
	}

	// Socket gone: detach silently, then start the grace window.
	g.hub.Detach(c)
	g.scheduleLeave(c)
}

// scheduleLeave fires the logical PlayerLeft after the reconnect window unless
// the player reconnects first (which cancels the timer).
func (g *Gateway) scheduleLeave(c *wsConn) {
	key := c.roomID + "|" + c.playerID
	player := core.Player{ID: c.playerID, DisplayName: c.display, RoomID: c.roomID}

	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.pending[key]; ok {
		t.Stop()
	}
	g.pending[key] = time.AfterFunc(g.reconnectWindow, func() {
		g.mu.Lock()
		delete(g.pending, key)
		g.mu.Unlock()
		g.hub.Leave(context.Background(), player)
	})
}

// cancelPendingLeave stops a pending PlayerLeft, returning true if one existed
// (i.e. this is a reconnect within the grace window).
func (g *Gateway) cancelPendingLeave(roomID, playerID string) bool {
	key := roomID + "|" + playerID
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.pending[key]; ok {
		t.Stop()
		delete(g.pending, key)
		return true
	}
	return false
}
