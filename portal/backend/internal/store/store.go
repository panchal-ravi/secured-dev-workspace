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

// Project descriptor lifecycle statuses: a descriptor is provisioning until its
// engines are up, then ready; error records a failed provision so a launch is not
// attempted mid-provision.
const (
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusError        = "error"
)

// MCPServer is a platform-deployed MCP server: the deploy definition (image + run
// config a Platform Admin transcribed from the server's Docker/K8s instructions)
// plus its lifecycle state on the platform.
type MCPServer struct {
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	Command          []string          `json:"command,omitempty"`
	Env              map[string]string `json:"env,omitempty"`                // non-secret env values
	SecretRefs       map[string]string `json:"secret_refs,omitempty"`        // env key -> Vault KV "path#field" reference
	InjectVaultToken bool              `json:"inject_vault_token,omitempty"` // emit a bare vault{role} so Nomad injects a WIF VAULT_TOKEN (Vault-auth servers)
	BlueprintRef     *BlueprintRef     `json:"blueprint_ref,omitempty"`      // bound at publish for blueprint-backed server types
	Transport        string            `json:"transport"`                    // stdio | sse | streamable-http
	Port             int               `json:"port,omitempty"`
	Path             string            `json:"path,omitempty"`
	Namespace        string            `json:"namespace"`
	JobID            string            `json:"job_id,omitempty"`
	PeerID           string            `json:"peer_id,omitempty"`
	GatewayURL       string            `json:"gateway_url,omitempty"`
	Status           string            `json:"status"`
	Version          int               `json:"version"`
	TestResult       *MCPTestResult    `json:"test_result,omitempty"`
	CreatedBy        string            `json:"created_by,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
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
	Transport    string          `json:"transport,omitempty"` // sse | streamable-http; passed to the gateway at RegisterPeer
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

// ProjectDescriptor is a project's portal descriptor row: the contract the portal
// reads to render project cards, gate access (developers group vs the logged-in
// developer's IBM Verify groups), and drive workspace creation. Moved off Vault KV
// (secret/data/projects/<p>/portal-descriptor) so all portal control-plane state
// lives in Postgres. Descriptor holds the full descriptor.Descriptor JSON, opaque
// to the store (parsed by consumers); no secret material is stored.
type ProjectDescriptor struct {
	Project    string          `json:"project"`
	Status     string          `json:"status"`
	Descriptor json.RawMessage `json:"descriptor"`
	CreatedBy  string          `json:"created_by,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// BaseJobTemplate is a platform-authored generic Nomad workspace job template
// (standard / GPU / microVM), seeded from go:embed and editable by a portal-admin.
// It is MUTABLE with a version that bumps on publish; a project template snapshots
// its PublishedSource at create time, so editing a base never disturbs live
// projects. Source carries the ${...} placeholders (10 project-static + 5
// per-workspace); no secret material is stored.
type BaseJobTemplate struct {
	Name            string    `json:"name"`
	Label           string    `json:"label,omitempty"`
	Description     string    `json:"description,omitempty"`
	Status          string    `json:"status"`  // draft | published
	Version         int       `json:"version"` // bumps on publish
	ContentHash     string    `json:"content_hash,omitempty"`
	DraftSource     string    `json:"draft_source"`     // editable HCL
	PublishedSource string    `json:"published_source"` // frozen at last publish
	Image           string    `json:"image,omitempty"`  // container image baked into project templates (portal-admin owned)
	Features        []Feature `json:"features,omitempty"`
	DefaultNodePool string    `json:"default_node_pool,omitempty"`
	Runtime         string    `json:"runtime,omitempty"` // "", "nvidia", "kata" (informational)
	CreatedBy       string    `json:"created_by,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Feature is one capability a flavor surfaces in the workspace card. Mirrors
// descriptor.Feature without the package dependency.
type Feature struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// ProjectTemplate is a per-project "flavor": a snapshot of a base template's
// PublishedSource with the 10 project-static placeholders baked in (pass-1), the
// per-workspace ${...} tokens left for jobrender at launch. Created by a
// project-admin (Phase C) or backfilled from Vault KV. No secret material.
type ProjectTemplate struct {
	Project        string    `json:"project"`
	Flavor         string    `json:"flavor"`       // base template name it derives from
	BaseVersion    int       `json:"base_version"` // BaseJobTemplate.Version snapshotted
	Status         string    `json:"status"`
	RenderedSource string    `json:"rendered_source"`      // pass-1 baked + add-ons injected (what launch renders pass-2)
	BakedBase      string    `json:"baked_base,omitempty"` // pass-1 baked with @project-addons markers intact (snapshot; re-injected on add-on change)
	Label          string    `json:"label,omitempty"`
	Description    string    `json:"description,omitempty"`
	Image          string    `json:"image,omitempty"`
	GitRepoURL     string    `json:"git_repo_url,omitempty"`
	NodePool       string    `json:"node_pool,omitempty"`
	Features       []Feature `json:"features,omitempty"`
	// Addons is the project-admin's structured extension of the base template: MCP
	// servers (from the catalog) and extra secret engines wired into the workspace.
	// The RenderedSource above is (re)generated from the base + these add-ons.
	Addons    TemplateAddons `json:"addons,omitempty"`
	CreatedBy string         `json:"created_by,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// TemplateAddons is the project-specific extension layer injected into a base
// template at the @project-addons markers. No secret material (secrets are read at
// workspace runtime from Vault over WIF).
type TemplateAddons struct {
	MCPServers []string      `json:"mcp_servers,omitempty"` // deployed MCP server names to register in the workspace
	Engines    []AddonEngine `json:"engines,omitempty"`     // extra Vault secret engines wired into the workspace
}

// AddonEngine is an extra Vault secret engine a project-admin mounts in the project
// namespace and surfaces in the workspace as one or more /secrets files.
type AddonEngine struct {
	Mount       string            `json:"mount"`                  // Vault mount path (e.g. "kv-tools")
	Type        string            `json:"type"`                   // engine type (e.g. "kv-v2")
	KVPath      string            `json:"kv_path,omitempty"`      // Vault read path the workspace consumes
	SecretFiles []AddonSecretFile `json:"secret_files,omitempty"` // field → /secrets file (+ optional env)
}

// AddonSecretFile maps one Vault KV field to a /secrets file the workspace reads.
type AddonSecretFile struct {
	KVField  string `json:"kv_field"`      // field under the KV path (KV v2 .Data.data.<field>)
	DestFile string `json:"dest_file"`     // /secrets file name written by consul-template
	Env      string `json:"env,omitempty"` // optional env var exported in the entrypoint
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
	DeleteBlueprint(ctx context.Context, id string, version int) error

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

	// Project descriptors (the portal's per-project contract; was Vault KV).
	UpsertProjectDescriptor(ctx context.Context, d ProjectDescriptor) (ProjectDescriptor, error)
	GetProjectDescriptor(ctx context.Context, project string) (ProjectDescriptor, error)
	ListProjectDescriptors(ctx context.Context) ([]ProjectDescriptor, error)
	DeleteProjectDescriptor(ctx context.Context, project string) error

	// Base job templates (platform-authored; was terraform/project templates + KV).
	UpsertBaseJobTemplate(ctx context.Context, t BaseJobTemplate) (BaseJobTemplate, error)
	GetBaseJobTemplate(ctx context.Context, name string) (BaseJobTemplate, error)
	ListBaseJobTemplates(ctx context.Context) ([]BaseJobTemplate, error)
	DeleteBaseJobTemplate(ctx context.Context, name string) error

	// Project templates (per-project flavors; was Vault KV job-templates/<flavor>).
	UpsertProjectTemplate(ctx context.Context, t ProjectTemplate) (ProjectTemplate, error)
	GetProjectTemplate(ctx context.Context, project, flavor string) (ProjectTemplate, error)
	ListProjectTemplates(ctx context.Context, project string) ([]ProjectTemplate, error)
	DeleteProjectTemplate(ctx context.Context, project, flavor string) error

	// Admin audit log.
	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, limit int) ([]AuditEvent, error)
}
