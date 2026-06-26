package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

type ctxKey int

const userIDKey ctxKey = 0

// UserID extracts the authenticated user id placed by Middleware. Empty if none.
func UserID(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey).(string)
	return id
}

// Handler exposes the auth REST endpoints.
type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers POST /login, /logout, /refresh on the given mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /login", h.login)
	mux.HandleFunc("POST /logout", h.logout)
	mux.HandleFunc("POST /refresh", h.refresh)
}

type loginReq struct {
	Mode        string `json:"mode"` // "guest" | "email"
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

type loginResp struct {
	User   *User   `json:"user"`
	Tokens *Tokens `json:"tokens"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	var (
		u   *User
		t   *Tokens
		err error
	)
	switch req.Mode {
	case "email":
		u, t, err = h.svc.EmailLogin(r.Context(), req.Email, req.DisplayName)
	default: // guest
		u, t, err = h.svc.GuestLogin(r.Context(), req.DisplayName)
	}
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, loginResp{User: u, Tokens: t})
}

type tokenReq struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var req tokenReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	t, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var req tokenReq
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if err := h.svc.Logout(r.Context(), req.RefreshToken); err != nil {
		writeErr(w, http.StatusInternalServerError, "logout failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Middleware authenticates requests via the Authorization: Bearer <jwt> header
// and stores the user id in the request context. Unauthenticated requests are
// rejected with 401.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := h.authenticate(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, id)))
	})
}

// Authenticate validates the bearer token on r, returning the user id. Exposed
// so the WS gateway can authenticate the upgrade handshake.
func (h *Handler) authenticate(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "" || token == header {
		// also accept ?token= for WebSocket clients that can't set headers
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return "", false
	}
	id, err := h.svc.Verify(token)
	if err != nil {
		return "", false
	}
	return id, true
}

// Authenticate is the exported form used by other packages (e.g. gateway).
func (h *Handler) Authenticate(r *http.Request) (string, bool) { return h.authenticate(r) }

// Optional authenticates if a valid token is present but never rejects. Used for
// endpoints that work anonymously yet enrich behavior when a user is known
// (e.g. setting room owner). Phase 1 keeps these open for the dashboard.
func (h *Handler) Optional(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := h.authenticate(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), userIDKey, id))
		}
		next.ServeHTTP(w, r)
	})
}

// --- small json helpers shared by handlers ---

func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil // empty body is allowed (e.g. guest login)
	}
	return err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
