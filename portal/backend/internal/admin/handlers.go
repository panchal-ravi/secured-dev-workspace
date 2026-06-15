package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
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
	mux.Handle("GET /api/admin/mcp-servers", protect(h.listMCPServers))
	mux.Handle("POST /api/admin/mcp-servers", mutate(h.deployMCPServer))
	mux.Handle("POST /api/admin/mcp-servers/{name}/test", mutate(h.testMCPServer))
	mux.Handle("POST /api/admin/mcp-servers/{name}/publish", mutate(h.publishMCPServer))
	mux.Handle("DELETE /api/admin/mcp-servers/{name}", mutate(h.deleteMCPServer))

	mux.Handle("GET /api/admin/llm/models", protect(h.listLLMModels))
	mux.Handle("POST /api/admin/llm/models", mutate(h.onboardLLMModel))
	mux.Handle("POST /api/admin/llm/models/{name}/test", mutate(h.testLLMModel))
	mux.Handle("POST /api/admin/llm/models/{name}/publish", mutate(h.publishLLMModel))
	mux.Handle("DELETE /api/admin/llm/models/{name}", mutate(h.deleteLLMModel))

	mux.Handle("GET /api/admin/blueprints", protect(h.listBlueprints))
	mux.Handle("POST /api/admin/blueprints", mutate(h.createBlueprint))
	mux.Handle("POST /api/admin/blueprints/{id}/{version}/validate", mutate(h.validateBlueprint))
	mux.Handle("POST /api/admin/blueprints/{id}/{version}/publish", mutate(h.publishBlueprint))

	mux.Handle("GET /api/admin/audit", protect(h.listAudit))
}

// ---- MCP server handlers ----

func (h *Handlers) listMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.svc.ListMCPServers(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": servers})
}

func (h *Handlers) deployMCPServer(w http.ResponseWriter, r *http.Request) {
	var in DeployMCPInput
	if !decode(w, r, &in) {
		return
	}
	srv, err := h.svc.DeployMCPServer(r.Context(), actor(r), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, srv)
}

func (h *Handlers) testMCPServer(w http.ResponseWriter, r *http.Request) {
	srv, err := h.svc.TestMCPServer(r.Context(), actor(r), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (h *Handlers) publishMCPServer(w http.ResponseWriter, r *http.Request) {
	srv, err := h.svc.PublishMCPServer(r.Context(), actor(r), r.PathValue("name"))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, srv)
}

func (h *Handlers) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.DeleteMCPServer(r.Context(), actor(r), r.PathValue("name")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- LLM model handlers ----

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

// ---- credential blueprint handlers ----

func (h *Handlers) listBlueprints(w http.ResponseWriter, r *http.Request) {
	bps, err := h.svc.ListBlueprints(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"blueprints": bps})
}

func (h *Handlers) createBlueprint(w http.ResponseWriter, r *http.Request) {
	var m blueprint.BlueprintManifest
	if !decode(w, r, &m) {
		return
	}
	bp, err := h.svc.CreateBlueprintDraft(r.Context(), actor(r), m)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, bp)
}

func (h *Handlers) validateBlueprint(w http.ResponseWriter, r *http.Request) {
	ver, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "version must be an integer", middleware.RequestID(r.Context()))
		return
	}
	bp, err := h.svc.ValidateBlueprint(r.Context(), actor(r), r.PathValue("id"), ver)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bp)
}

func (h *Handlers) publishBlueprint(w http.ResponseWriter, r *http.Request) {
	ver, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "version must be an integer", middleware.RequestID(r.Context()))
		return
	}
	bp, err := h.svc.PublishBlueprint(r.Context(), actor(r), r.PathValue("id"), ver)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bp)
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
