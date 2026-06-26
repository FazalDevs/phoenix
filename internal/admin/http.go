// Package admin exposes the operational API consumed by the Next.js dashboard:
// live metrics, room listing/termination, event inspection, replay data, and
// player bans. Phase 1 leaves these endpoints open (no auth).
package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/fazal/phoenix/internal/bots"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/eventbus"
	"github.com/fazal/phoenix/internal/httpx"
	"github.com/fazal/phoenix/internal/room"
	"github.com/fazal/phoenix/internal/state"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Handler struct {
	store    core.EventStore
	rooms    *room.Service
	hub      *state.Hub
	pool     *pgxpool.Pool
	bus      eventbus.Bus
	presence core.PresenceStore // may be nil if Redis is unavailable
	bots     *bots.Runner
}

func NewHandler(store core.EventStore, rooms *room.Service, hub *state.Hub, pool *pgxpool.Pool, bus eventbus.Bus, presence core.PresenceStore, botRunner *bots.Runner) *Handler {
	return &Handler{store: store, rooms: rooms, hub: hub, pool: pool, bus: bus, presence: presence, bots: botRunner}
}

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/metrics", h.metrics)
	mux.HandleFunc("GET /admin/rooms", h.listRooms)
	mux.HandleFunc("GET /admin/rooms/{id}/events", h.events)
	mux.HandleFunc("GET /admin/rooms/{id}/stream", h.stream)
	mux.HandleFunc("GET /admin/rooms/{id}/state", h.roomState)
	mux.HandleFunc("POST /admin/rooms/{id}/terminate", h.terminate)
	mux.HandleFunc("POST /admin/users/{id}/ban", h.ban)
	mux.HandleFunc("POST /admin/demo", h.demo)
}

// demo spawns a room of wandering arena bots so the dashboard is lively on
// demand. POST /admin/demo?bots=8 -> { room, ws }.
func (h *Handler) demo(w http.ResponseWriter, r *http.Request) {
	if h.bots == nil {
		httpx.WriteErr(w, http.StatusServiceUnavailable, "bots unavailable")
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("bots"))
	if n == 0 {
		n = 8
	}
	roomID, err := h.bots.LaunchArena(n, 60*time.Second)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "demo failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"room": roomID, "ws": "/ws?room=" + roomID})
}

// metrics returns live counters for the dashboard: live socket stats from the
// hub, presence-online from the Redis projection, and total events published to
// the bus (event-throughput signal).
func (h *Handler) metrics(w http.ResponseWriter, r *http.Request) {
	s := h.hub.Stats()
	out := map[string]any{
		"active_rooms":     s.ActiveRooms,
		"online_players":   s.OnlinePlayers, // live sockets
		"per_room":         s.PerRoom,
		"events_published": int64(0),
		"presence_online":  0,
	}
	if h.bus != nil {
		out["events_published"] = h.bus.Stats().Published
	}
	if h.presence != nil {
		if n, err := h.presence.OnlineCount(context.Background()); err == nil {
			out["presence_online"] = n
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// listRooms returns all rooms including closed (admin sees everything).
func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	rooms, err := h.rooms.List(r.Context(), true)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	if rooms == nil {
		rooms = []core.Room{}
	}
	httpx.WriteJSON(w, http.StatusOK, rooms)
}

// events returns the event log slice for a room — this is both the event
// inspector and the raw feed for the replay engine (replay = these events
// re-applied in Seq order).
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	from, _ := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if from <= 0 {
		from = 1
	}
	events, err := h.store.Load(r.Context(), r.PathValue("id"), from, to)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	if events == nil {
		events = []core.Event{}
	}
	httpx.WriteJSON(w, http.StatusOK, events)
}

// roomState returns the current reduced state of a live room.
func (h *Handler) roomState(w http.ResponseWriter, r *http.Request) {
	st, ok := h.hub.CurrentState(r.PathValue("id"))
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": false})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"active": true, "state": st})
}

func (h *Handler) terminate(w http.ResponseWriter, r *http.Request) {
	if err := h.rooms.Close(r.Context(), r.PathValue("id")); err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ban(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `UPDATE users SET banned=true WHERE id=$1`, r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "ban failed")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteErr(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
