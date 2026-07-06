package projectbootstrap

import (
	"encoding/json"
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

// Register mounts the project-create/detail/edit routes. protect applies auth +
// the platform-admin gate; mutate additionally rate-limits (wired in api.NewMux).
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /api/admin/projects", mutate(h.createProject))
	mux.Handle("GET /api/admin/projects/{name}", protect(h.getProject))
	mux.Handle("PUT /api/admin/projects/{name}", mutate(h.updateProject))
	mux.Handle("DELETE /api/admin/projects/{name}", mutate(h.deleteProject))
}

func (h *Handlers) createProject(w http.ResponseWriter, r *http.Request) {
	var in CreateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	d, err := h.svc.CreateProject(r.Context(), actor(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handlers) getProject(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetProject(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) updateProject(w http.ResponseWriter, r *http.Request) {
	var in UpdateProjectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	d, err := h.svc.UpdateProject(r.Context(), actor(r), r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteProject(r.Context(), actor(r), r.PathValue("name")); err != nil {
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
	slog.Error("project-create request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
