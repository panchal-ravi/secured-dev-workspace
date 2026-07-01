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
	"encoding/json"
	"time"
)

// Lifecycle statuses. An MCP server goes draft → deployed → published; an LLM
// model goes draft → published. Only published capabilities are visible to
// Project Admins.
const (
	StatusDraft     = "draft"
	StatusDeployed  = "deployed"
	StatusValidated = "validated"
	StatusPublished = "published"
)

// MCPServer is a platform-deployed MCP server: the deploy definition (image + run
// config a Platform Admin transcribed from the server's Docker/K8s instructions)
// plus its lifecycle state on the platform.
type MCPServer struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Command      []string          `json:"command,omitempty"`
	Env          map[string]string `json:"env,omitempty"`           // non-secret env values
	SecretRefs   map[string]string `json:"secret_refs,omitempty"`   // env key -> Vault KV "path#field" reference
	BlueprintRef *BlueprintRef     `json:"blueprint_ref,omitempty"` // bound at publish for blueprint-backed server types
	Transport    string            `json:"transport"`               // stdio | sse | streamable-http
	Port         int               `json:"port,omitempty"`
	Path         string            `json:"path,omitempty"`
	Namespace    string            `json:"namespace"`
	JobID        string            `json:"job_id,omitempty"`
	PeerID       string            `json:"peer_id,omitempty"`
	GatewayURL   string            `json:"gateway_url,omitempty"`
	Status       string            `json:"status"`
	Version      int               `json:"version"`
	TestResult   *MCPTestResult    `json:"test_result,omitempty"`
	CreatedBy    string            `json:"created_by,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// BlueprintRef is the immutable pin (id, version, content-hash) of the credential
// blueprint a server type is bound to. Mirrors blueprint.BlueprintRef without the
// package dependency.
type BlueprintRef struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

// ProjectMCPServer is an MCP server a project-admin deployed into their project,
// with the blueprint instance that brokered its credential. Instance is the opaque
// blueprint.InstanceRecord JSON, persisted so deprovision can revoke exactly what
// was created. No secret material is stored.
type ProjectMCPServer struct {
	Project      string          `json:"project"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	BlueprintRef BlueprintRef    `json:"blueprint_ref"`
	Instance     json.RawMessage `json:"instance,omitempty"`
	JobID        string          `json:"job_id,omitempty"`
	PeerID       string          `json:"peer_id,omitempty"`
	GatewayURL   string          `json:"gateway_url,omitempty"`
	TestResult   *MCPTestResult  `json:"test_result,omitempty"`
	CreatedBy    string          `json:"created_by,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
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

// Blueprint is a platform-authored credential blueprint's control-plane record.
// The canonical manifest JSON now lives on this row (the Manifest field), not in
// Vault KV; the row carries identity, lifecycle, the content-hash pin, the manifest,
// and the validation result. No secret material is stored here.
type Blueprint struct {
	ID          string          `json:"id"`
	Version     int             `json:"version"`
	Class       string          `json:"class"`
	ContentHash string          `json:"content_hash"`
	Status      string          `json:"status"`
	Validation  json.RawMessage `json:"validation,omitempty"` // a blueprint.ValidationResult, opaque to the store
	Manifest    json.RawMessage `json:"manifest,omitempty"`   // canonical blueprint.BlueprintManifest JSON; immutable per (id,version)
	CreatedBy   string          `json:"created_by,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ProjectRole is a project-scoped role elevation: a project member (in the
// project's IBM Verify developers group) granted an in-app role. Membership stays
// in the IdP; this table records only the elevation. No secret material.
type ProjectRole struct {
	Project   string    `json:"project"`
	Subject   string    `json:"subject"` // lowercased email
	Role      string    `json:"role"`
	GrantedBy string    `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
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

	// Credential blueprints (platform-authored).
	UpsertBlueprint(ctx context.Context, b Blueprint) (Blueprint, error)
	GetBlueprint(ctx context.Context, id string, version int) (Blueprint, error)
	ListBlueprints(ctx context.Context) ([]Blueprint, error)

	// Project role elevations (group membership stays in IBM Verify).
	GrantProjectRole(ctx context.Context, pr ProjectRole) (ProjectRole, error)
	RevokeProjectRole(ctx context.Context, project, subject, role string) error
	HasProjectRole(ctx context.Context, project, subject, role string) (bool, error)
	ListProjectRoles(ctx context.Context, project string) ([]ProjectRole, error)
	ProjectRolesForSubject(ctx context.Context, subject string) ([]ProjectRole, error)

	// Project-deployed MCP servers (blueprint-instantiated).
	UpsertProjectMCPServer(ctx context.Context, s ProjectMCPServer) (ProjectMCPServer, error)
	GetProjectMCPServer(ctx context.Context, project, name string) (ProjectMCPServer, error)
	ListProjectMCPServers(ctx context.Context, project string) ([]ProjectMCPServer, error)
	DeleteProjectMCPServer(ctx context.Context, project, name string) error

	// Admin audit log.
	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, limit int) ([]AuditEvent, error)
}
