package projectengines

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

// Register wires the project-admin engine routes (project-admin gated; mutations
// rate-limited): re-provision, the deferred GitHub-credentials set/update, and the
// non-secret engine status the Engines page reads.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /api/projects/{name}/provision", mutate(h.Provision))
	mux.Handle("GET /api/projects/{name}/engines", protect(h.status))
	mux.Handle("POST /api/projects/{name}/engines/github", mutate(h.setGithub))
}

func (h *Handlers) Provision(w http.ResponseWriter, r *http.Request) {
	var in ProvisionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	d, err := h.svc.Provision(r.Context(), u.Email, u.Groups, r.PathValue("name"), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) status(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	st, err := h.svc.Status(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *Handlers) setGithub(w http.ResponseWriter, r *http.Request) {
	var in ProvisionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return
	}
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.SetGitHubCredentials(r.Context(), u.Email, u.Groups, r.PathValue("name"), in); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- helpers (mirror the projectadmin adapter) ----

func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("projectengines request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
