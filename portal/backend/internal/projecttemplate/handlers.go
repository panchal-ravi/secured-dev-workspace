package projecttemplate

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Handlers is the thin HTTP adapter over Service.
type Handlers struct{ svc *Service }

// NewHandlers wraps a Service in its HTTP adapter.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Register wires the /api/projects/{name}/templates routes. protect/mutate add
// auth + project-admin gating (and rate-limit for mutate) at the mux.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/projects/{name}/base-templates", protect(h.ListBase))
	mux.Handle("GET /api/projects/{name}/coding-agents", protect(h.ListCodingAgents))
	mux.Handle("GET /api/projects/{name}/templates", protect(h.List))
	mux.Handle("POST /api/projects/{name}/templates", mutate(h.Create))
	mux.Handle("PUT /api/projects/{name}/templates/{flavor}", mutate(h.Update))
	mux.Handle("PUT /api/projects/{name}/templates/{flavor}/addons", mutate(h.SetAddons))
	mux.Handle("DELETE /api/projects/{name}/templates/{flavor}", mutate(h.Delete))
}

func (h *Handlers) ListBase(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	bases, err := h.svc.ListBase(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bases)
}

func (h *Handlers) ListCodingAgents(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	agents, err := h.svc.ListCodingAgents(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	tmpls, err := h.svc.List(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tmpls)
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	pt, err := h.svc.Create(r.Context(), u.Email, u.Groups, r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, pt)
}

func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	var in UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	pt, err := h.svc.Update(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("flavor"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pt)
}

func (h *Handlers) SetAddons(w http.ResponseWriter, r *http.Request) {
	var in store.TemplateAddons
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	pt, err := h.svc.SetAddons(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("flavor"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pt)
}

func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.Delete(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("flavor")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers (mirror the projectadmin adapter) ----

func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("projecttemplate request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
