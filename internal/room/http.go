package room

import (
	"errors"
	"net/http"

	"github.com/fazal/phoenix/internal/auth"
	"github.com/fazal/phoenix/internal/core"
	"github.com/fazal/phoenix/internal/httpx"
)

// Handler exposes room REST endpoints. Mutating routes are expected to be
// wrapped in auth middleware so auth.UserID is populated.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers the room REST API. The mux pattern matching (Go 1.22+) gives
// us path params like {id}.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /rooms", h.create)
	mux.HandleFunc("GET /rooms", h.list)
	mux.HandleFunc("GET /rooms/{id}", h.get)
	mux.HandleFunc("POST /rooms/{id}/join", h.join)
	mux.HandleFunc("POST /rooms/{id}/leave", h.leave)
	mux.HandleFunc("DELETE /rooms/{id}", h.terminate)
}

type createReq struct {
	GameType   string `json:"game_type"`
	MaxPlayers int    `json:"max_players"`
	IsPrivate  bool   `json:"is_private"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := httpx.Decode(r, &req); err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, "bad request")
		return
	}
	rm, err := h.svc.Create(r.Context(), CreateParams{
		OwnerID:    auth.UserID(r.Context()),
		GameType:   req.GameType,
		MaxPlayers: req.MaxPlayers,
		IsPrivate:  req.IsPrivate,
	})
	if err != nil {
		httpx.WriteErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, rm)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	includeClosed := r.URL.Query().Get("all") == "true"
	rooms, err := h.svc.List(r.Context(), includeClosed)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	if rooms == nil {
		rooms = []core.Room{} // serialize empty as [] not null
	}
	httpx.WriteJSON(w, http.StatusOK, rooms)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	rm, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rm)
}

// join validates the room is joinable. The actual realtime join happens over
// the WebSocket; this endpoint lets clients pre-check before connecting.
func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	rm, err := h.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.WriteErr(w, http.StatusNotFound, "not found")
		return
	}
	if rm.Status == "closed" {
		httpx.WriteErr(w, http.StatusConflict, "room closed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"room": rm, "ws": "/ws?room=" + rm.ID})
}

func (h *Handler) leave(w http.ResponseWriter, r *http.Request) {
	// Leaving is realtime (close the socket). REST leave is a no-op acknowledgement.
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) terminate(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Close(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteErr(w, http.StatusNotFound, "not found")
			return
		}
		httpx.WriteErr(w, http.StatusInternalServerError, "terminate failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
