package matchmaking

import (
	"net/http"

	"github.com/fazal/phoenix/internal/auth"
	"github.com/fazal/phoenix/internal/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /matchmake", h.quickMatch)
}

// quickMatch pairs the caller into a room: POST /matchmake?game=chess.
func (h *Handler) quickMatch(w http.ResponseWriter, r *http.Request) {
	game := r.URL.Query().Get("game")
	rm, err := h.svc.QuickMatch(r.Context(), game, auth.UserID(r.Context()))
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "matchmaking failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"room": rm,
		"ws":   "/ws?room=" + rm.ID,
	})
}
