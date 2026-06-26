package leaderboard

import (
	"net/http"
	"strconv"

	"github.com/fazal/phoenix/internal/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /leaderboard", h.top)
}

func (h *Handler) top(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	standings, err := h.svc.Top(r.Context(), n)
	if err != nil {
		httpx.WriteErr(w, http.StatusInternalServerError, "leaderboard unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, standings)
}
