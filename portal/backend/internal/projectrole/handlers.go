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
	// role is an optional query param; empty defaults to project-admin in Service.
	role := r.URL.Query().Get("role")
	if err := h.svc.Revoke(r.Context(), actor(r), r.PathValue("name"), r.PathValue("subject"), role); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetCapabilities returns the project's role→capability matrix; is_default
// tells the UI whether the project has stored its own row yet.
func (h *Handlers) GetCapabilities(w http.ResponseWriter, r *http.Request) {
	matrix, isDefault, err := h.svc.GetCapabilities(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matrix": matrix, "is_default": isDefault})
}

func (h *Handlers) PutCapabilities(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Matrix map[string][]string `json:"matrix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	if err := h.svc.SetCapabilities(r.Context(), actor(r), r.PathValue("name"), in.Matrix); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"matrix": in.Matrix, "is_default": false})
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
