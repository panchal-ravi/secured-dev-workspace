package projectrole

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

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.List(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (h *Handlers) Grant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject string `json:"subject"`
		Role    string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	pr, err := h.svc.Grant(r.Context(), actor(r), r.PathValue("name"), in.Subject, in.Role)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pr)
}

func (h *Handlers) Revoke(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Revoke(r.Context(), actor(r), r.PathValue("name"), r.PathValue("subject")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

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
	slog.Error("project-role request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
