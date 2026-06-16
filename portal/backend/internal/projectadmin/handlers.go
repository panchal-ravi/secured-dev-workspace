package projectadmin

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
type Handlers struct{ svc *Service }

// NewHandlers wraps a Service in its HTTP adapter.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Register wires the /api/projects/{name}/mcp-servers routes. protect/mutate add
// auth + project-admin gating (and rate-limit for mutate) at the mux.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/projects/{name}/mcp-servers", protect(h.List))
	mux.Handle("POST /api/projects/{name}/mcp-servers", mutate(h.Deploy))
	mux.Handle("POST /api/projects/{name}/mcp-servers/{server}/test", mutate(h.Test))
	mux.Handle("DELETE /api/projects/{name}/mcp-servers/{server}", mutate(h.Delete))
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	cat, err := h.svc.ListDeployable(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cat)
}

func (h *Handlers) Deploy(w http.ResponseWriter, r *http.Request) {
	var in DeployInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	srv, err := h.svc.DeployServer(r.Context(), u.Email, u.Groups, r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, srv)
}

func (h *Handlers) Test(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	srv, err := h.svc.TestServer(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("server"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.DeleteServer(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("server")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers (mirror the admin/projectrole adapters) ----

func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("projectadmin request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
