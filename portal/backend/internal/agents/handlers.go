package agents

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// Handlers is the thin HTTP adapter over Service for the project-admin template
// plane. The per-user instance plane has its own adapter (instances.go).
type Handlers struct{ svc *Service }

// NewHandlers wraps a Service in its HTTP adapter.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Register wires the project-admin /agents/templates routes. protect/mutate add auth
// + the project-admin gate (and rate-limit for mutate) at the mux.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/projects/{name}/agents/templates", protect(h.List))
	mux.Handle("POST /api/projects/{name}/agents/templates", mutate(h.Save))
	mux.Handle("POST /api/projects/{name}/agents/templates/validate", mutate(h.Validate))
	mux.Handle("GET /api/projects/{name}/agents/templates/{tmpl}", protect(h.Get))
	mux.Handle("PUT /api/projects/{name}/agents/templates/{tmpl}", mutate(h.Save))
	mux.Handle("DELETE /api/projects/{name}/agents/templates/{tmpl}", mutate(h.Delete))
	mux.Handle("POST /api/projects/{name}/agents/templates/{tmpl}/deploy", mutate(h.Deploy))
	mux.Handle("POST /api/projects/{name}/agents/templates/{tmpl}/test", mutate(h.Test))
	mux.Handle("POST /api/projects/{name}/agents/templates/{tmpl}/publish", mutate(h.Publish))
	mux.Handle("POST /api/projects/{name}/agents/templates/{tmpl}/chat", protect(h.Chat))
}

// RegisterInstances wires the project-user instance plane. protect/mutate add auth
// + the ai-agents capability gate (mutate also rate-limits) at the mux. A project
// selects instances by the caller's subject, so ownership needs no path parameter.
func (h *Handlers) RegisterInstances(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("GET /api/projects/{name}/agents/cards", protect(h.ListCards))
	mux.Handle("GET /api/projects/{name}/agents/instances", protect(h.ListInstances))
	mux.Handle("POST /api/projects/{name}/agents/instances/{tmpl}/chat", mutate(h.ChatInstance))
	mux.Handle("DELETE /api/projects/{name}/agents/instances/{tmpl}", mutate(h.DeleteInstance))
}

func (h *Handlers) ListCards(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	cards, err := h.svc.ListCards(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards})
}

func (h *Handlers) ListInstances(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	insts, err := h.svc.ListInstances(r.Context(), u.Groups, r.PathValue("name"), u.Email)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"instances": insts})
}

// ChatInstance starts the caller's instance on demand and streams its SSE reply.
func (h *Handlers) ChatInstance(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	resp, err := h.svc.ChatInstance(r.Context(), u.Groups, r.PathValue("name"), r.PathValue("tmpl"), u.Email, r.Body)
	if err != nil {
		fail(w, r, err)
		return
	}
	streamSSE(w, resp)
}

func (h *Handlers) DeleteInstance(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.DeleteInstance(r.Context(), u.Groups, r.PathValue("name"), r.PathValue("tmpl"), u.Email); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// yamlBody is the request shape for save/validate: the raw template YAML.
type yamlBody struct {
	YAML string `json:"yaml"`
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	out, err := h.svc.ListTemplates(r.Context(), u.Groups, r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	t, err := h.svc.GetTemplate(r.Context(), u.Groups, r.PathValue("name"), r.PathValue("tmpl"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Validate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeYAML(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.ValidateYAML(r.Context(), u.Groups, r.PathValue("name"), in.YAML); err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

func (h *Handlers) Save(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeYAML(w, r)
	if !ok {
		return
	}
	u, _ := auth.UserFrom(r.Context())
	t, err := h.svc.Save(r.Context(), u.Email, u.Groups, r.PathValue("name"), in.YAML)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Deploy(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	t, err := h.svc.DeployTest(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("tmpl"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Test(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	t, err := h.svc.Test(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("tmpl"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Publish(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	t, err := h.svc.Publish(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("tmpl"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if err := h.svc.DeleteTemplate(r.Context(), u.Email, u.Groups, r.PathValue("name"), r.PathValue("tmpl")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Chat proxies the admin test job's SSE stream to the client so a template can be
// exercised before publishing.
func (h *Handlers) Chat(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	resp, err := h.svc.ChatTest(r.Context(), u.Groups, r.PathValue("name"), r.PathValue("tmpl"), r.Body)
	if err != nil {
		fail(w, r, err)
		return
	}
	streamSSE(w, resp)
}

// streamSSE copies an upstream SSE response body to the client with periodic flushes.
func streamSSE(w http.ResponseWriter, resp *http.Response) {
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(http.StatusBadGateway)
	}
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// ---- helpers ----

func decodeYAML(w http.ResponseWriter, r *http.Request) (yamlBody, bool) {
	var in yamlBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return yamlBody{}, false
	}
	return in, true
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("agents request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
