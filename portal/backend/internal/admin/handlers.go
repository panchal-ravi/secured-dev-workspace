package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// Handlers is the thin HTTP adapter over Service. It carries no logic of its own —
// it decodes input, calls the service, and maps the result/error to JSON.
type Handlers struct {
	svc *Service
}

// NewHandlers wraps a Service in its HTTP adapter.
func NewHandlers(svc *Service) *Handlers { return &Handlers{svc: svc} }

// Register wires the /api/admin/* routes onto mux. protect applies auth + platform
// -admin gating to read routes; mutate adds rate limiting for state-changing ones.
func (h *Handlers) Register(mux *http.ServeMux, protect, mutate func(http.HandlerFunc) http.Handler) {
	mux.Handle("POST /api/admin/llm/providers", mutate(h.setProviderKey))
	mux.Handle("GET /api/admin/llm/models", protect(h.listLLMModels))
	mux.Handle("POST /api/admin/llm/models", mutate(h.onboardLLMModel))
	mux.Handle("POST /api/admin/llm/models/{name}/test", mutate(h.testLLMModel))
	mux.Handle("POST /api/admin/llm/models/{name}/publish", mutate(h.publishLLMModel))
	mux.Handle("DELETE /api/admin/llm/models/{name}", mutate(h.deleteLLMModel))

	mux.Handle("GET /api/admin/audit", protect(h.listAudit))
}

// ---- LLM model handlers ----

// setProviderKey stores a provider API key in Vault. Write-only: it returns 204
// with no body so the key is never reflected back to the client.
func (h *Handlers) setProviderKey(w http.ResponseWriter, r *http.Request) {
	var in SetProviderKeyInput
	if !decode(w, r, &in) {
		return
	}
	if err := h.svc.SetProviderKey(r.Context(), actor(r), in); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) listLLMModels(w http.ResponseWriter, r *http.Request) {
	models, err := h.svc.ListLLMModels(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h *Handlers) onboardLLMModel(w http.ResponseWriter, r *http.Request) {
	var in OnboardLLMInput
	if !decode(w, r, &in) {
		return
	}
	model, err := h.svc.OnboardLLMModel(r.Context(), actor(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, model)
}

func (h *Handlers) testLLMModel(w http.ResponseWriter, r *http.Request) {
	model, err := h.svc.TestLLMModel(r.Context(), actor(r), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, model)
}

func (h *Handlers) publishLLMModel(w http.ResponseWriter, r *http.Request) {
	model, err := h.svc.PublishLLMModel(r.Context(), actor(r), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, model)
}

func (h *Handlers) deleteLLMModel(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteLLMModel(r.Context(), actor(r), r.PathValue("name")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) listAudit(w http.ResponseWriter, r *http.Request) {
	events, err := h.svc.ListAudit(r.Context(), 100)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// ---- helpers ----

func actor(r *http.Request) string {
	u, _ := auth.UserFrom(r.Context())
	return u.Email
}

// decode reads a JSON body into v, writing a 400 and returning false on malformed
// input. An empty body is treated as malformed for these endpoints (all take input).
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "invalid request body", middleware.RequestID(r.Context()))
		return false
	}
	return true
}

// fail mirrors the api layer: known apperr classes return their status + safe
// message; anything else is logged and returned as a generic 502.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if status := apperr.Status(err); status != 0 {
		writeErr(w, status, err.Error(), rid)
		return
	}
	slog.Error("admin request failed", "err", err, "request_id", rid, "method", r.Method, "path", r.URL.Path)
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
