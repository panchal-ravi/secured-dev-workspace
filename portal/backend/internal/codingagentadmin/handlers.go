package codingagentadmin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// Handlers is the thin HTTP adapter over Service.
type Handlers struct {
	svc *Service
}

// NewHandlers wraps a Service in its HTTP adapter.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Register wires /api/admin/coding-agents onto mux. protect applies auth +
// platform-admin gating to reads; mutate adds rate limiting to state changes.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/admin/coding-agents", protect(h.list))
	mux.Handle("PUT /api/admin/coding-agents/{key}", mutate(h.setEnabled))
}

func (h *Handlers) list(w http.ResponseWriter, r *http.Request) {
	agents, err := h.svc.List(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (h *Handlers) setEnabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	a, err := h.svc.SetEnabled(r.Context(), actor(r), r.PathValue("key"), in.Enabled)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// ---- helpers (mirror the other handler packages) ----

func actor(r *http.Request) string {
	u, _ := auth.UserFrom(r.Context())
	return u.Email
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("coding-agent request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
	writeErr(w, http.StatusBadGateway, "upstream service error", rid)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg, requestID string) {
	writeJSON(w, status, map[string]string{"error": msg, "request_id": requestID})
}
