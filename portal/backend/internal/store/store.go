// Package store holds the portal's control-plane state for the Platform Admin
// onboarding plane: MCP server deploy definitions, the LLM model registry, and an
// admin audit log. State here is metadata and references only — never secret
// material (provider keys, gateway tokens live in Vault/LiteLLM, not here).
//
// Store is an interface so the admin service can be tested against the in-memory
// implementation (NewMemory); a Postgres-backed implementation lands with the
// portal-postgres Nomad job and is a drop-in for the same interface.
package store

import (
	"context"
	"time"
)

// Lifecycle statuses. An MCP server goes draft → deployed → published; an LLM
// model goes draft → published. Only published capabilities are visible to
// Project Admins.
const (
	StatusDraft     = "draft"
	StatusDeployed  = "deployed"
	StatusPublished = "published"
)

// MCPServer is a platform-deployed MCP server: the deploy definition (image + run
// config a Platform Admin transcribed from the server's Docker/K8s instructions)
// plus its lifecycle state on the platform.
type MCPServer struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`         // non-secret env values
	SecretRefs map[string]string `json:"secret_refs,omitempty"` // env key -> Vault KV "path#field" reference
	Transport  string            `json:"transport"`             // stdio | sse | streamable-http
	Port       int               `json:"port,omitempty"`
	Path       string            `json:"path,omitempty"`
	Namespace  string            `json:"namespace"`
	JobID      string            `json:"job_id,omitempty"`
	PeerID     string            `json:"peer_id,omitempty"`
	GatewayURL string            `json:"gateway_url,omitempty"`
	Status     string            `json:"status"`
	Version    int               `json:"version"`
	TestResult *MCPTestResult    `json:"test_result,omitempty"`
	CreatedBy  string            `json:"created_by,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// MCPTestResult records the consumption-mirror verification of a deployed server.
type MCPTestResult struct {
	Passed             bool      `json:"passed"`
	ToolsDiscovered    int       `json:"tools_discovered"`
	OwnServerOK        bool      `json:"own_server_ok"`
	AdminDenied        bool      `json:"admin_denied"`
	OtherServerDenied  bool      `json:"other_server_denied"`
	OtherServerChecked bool      `json:"other_server_checked"`
	Message            string    `json:"message,omitempty"`
	At                 time.Time `json:"at"`
}

// LLMModel is a platform-onboarded model in the LiteLLM gateway plus its registry
// state. Provider keys are never stored here — only the provider name.
type LLMModel struct {
	Name         string         `json:"name"`
	Provider     string         `json:"provider"`
	BackendModel string         `json:"backend_model"` // litellm_params.model
	LiteLLMID    string         `json:"litellm_id,omitempty"`
	Status       string         `json:"status"`
	TestResult   *LLMTestResult `json:"test_result,omitempty"`
	CreatedBy    string         `json:"created_by,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// LLMTestResult records the scoped-key consumption-mirror verification.
type LLMTestResult struct {
	Passed            bool      `json:"passed"`
	CompletionOK      bool      `json:"completion_ok"`       // 200 through a scoped key
	RateLimitEnforced bool      `json:"rate_limit_enforced"` // 429 on rpm breach
	RevokeEnforced    bool      `json:"revoke_enforced"`     // 401 after revoke
	Message           string    `json:"message,omitempty"`
	At                time.Time `json:"at"`
}

// AuditEvent is one admin mutation: who did what to which target, and the outcome.
type AuditEvent struct {
	ID      int64          `json:"id"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target"`
	Outcome string         `json:"outcome"`
	Detail  map[string]any `json:"detail,omitempty"`
	At      time.Time      `json:"at"`
}

// Store is the control-plane persistence the admin plane depends on.
type Store interface {
	// MCP server deploy definitions.
	UpsertMCPServer(ctx context.Context, s MCPServer) (MCPServer, error)
	GetMCPServer(ctx context.Context, name string) (MCPServer, error)
	ListMCPServers(ctx context.Context) ([]MCPServer, error)
	DeleteMCPServer(ctx context.Context, name string) error

	// LLM model registry.
	UpsertLLMModel(ctx context.Context, m LLMModel) (LLMModel, error)
	GetLLMModel(ctx context.Context, name string) (LLMModel, error)
	ListLLMModels(ctx context.Context) ([]LLMModel, error)
	DeleteLLMModel(ctx context.Context, name string) error

	// Admin audit log.
	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, limit int) ([]AuditEvent, error)
}
