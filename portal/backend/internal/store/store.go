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

// Lifecycle statuses. A project MCP server is deployed on creation; an LLM
// model goes draft → published. Only published models are visible to projects.
const (
	StatusDraft     = "draft"
	StatusDeployed  = "deployed"
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

// BlueprintRef is the immutable pin (id, version, content-hash) of the retired
// platform-catalog blueprint a LEGACY project MCP row was deployed from. Mirrors
// blueprint.BlueprintRef without the package dependency; new rows never set it.
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
	Project string `json:"project"`
	Name    string `json:"name"`
	Status  string `json:"status"`

	// Server definition, authored by the project-admin in the deploy wizard.
	Image   string            `json:"image,omitempty"`
	Command []string          `json:"command,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Port    int               `json:"port,omitempty"` // container port; host side is dynamic
	Path    string            `json:"path,omitempty"`
	// Credential is the blueprint.CredentialSpec JSON with its ${param}
	// placeholders intact; Params holds ONLY non-secret deploy values. Secret
	// param values are used once at deploy and never persisted.
	Credential json.RawMessage   `json:"credential,omitempty"`
	Params     map[string]string `json:"params,omitempty"`

	// BlueprintRef is legacy: rows deployed from the retired platform catalog
	// (pre-Phase-F) carry it; wizard rows leave it nil.
	BlueprintRef *BlueprintRef   `json:"blueprint_ref,omitempty"`
	Instance     json.RawMessage `json:"instance,omitempty"`
	JobID        string          `json:"job_id,omitempty"`
	PeerID       string          `json:"peer_id,omitempty"`
	GatewayURL   string          `json:"gateway_url,omitempty"`
	Transport    string          `json:"transport,omitempty"` // sse | streamable-http; passed to the gateway at RegisterPeer
	TestResult   *MCPTestResult  `json:"test_result,omitempty"`
	// Tools is the gateway-discovered tool catalog (id/name/description), cached
	// at Test time so a template author can pick a per-tool subset without a live
	// gateway round-trip. Additive — absent on rows tested before this field.
	Tools     []MCPTool `json:"tools,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MCPTool is one tool a deployed MCP server exposes, as discovered by the
// ContextForge gateway. ID is the gateway tool id used to scope a virtual server.
type MCPTool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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

// ProjectAgent is a YAML-configured AI agent a project-user deployed into their
// project. YAMLSource is the power user's verbatim agent YAML (the single source
// of truth, re-parsed on each deploy); the flattened fields below are denormalized
// for listing. No secret material is stored — the per-agent LiteLLM key lives only
// in Vault KV, referenced by LLMKeyAlias.
type ProjectAgent struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version int    `json:"version"`

	YAMLSource  string           `json:"yaml_source"`
	Description string           `json:"description,omitempty"`
	Greeting    string           `json:"greeting,omitempty"`
	Model       string           `json:"model,omitempty"`
	MCPServers  []string         `json:"mcp_servers,omitempty"`
	JobID       string           `json:"job_id,omitempty"`
	Endpoint    string           `json:"endpoint,omitempty"`      // host:port resolved from placement
	LLMKeyAlias string           `json:"llm_key_alias,omitempty"` // LiteLLM virtual-key alias, deleted on agent delete
	TestResult  *AgentTestResult `json:"test_result,omitempty"`

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AgentTestResult records the /healthz probe of a deployed agent.
type AgentTestResult struct {
	Passed          bool      `json:"passed"`
	ToolsDiscovered int       `json:"tools_discovered"`
	Message         string    `json:"message,omitempty"`
	At              time.Time `json:"at"`
}

// ProjectAgentTemplate is a project-admin-authored agent blueprint: system prompt,
// model, and a per-server tool subset, authored as YAML. The admin deploy-tests a
// template, then publishes it as an "agent card" that project-users instantiate
// (see ProjectAgentInstance). YAMLSource is the source of truth; the flattened
// fields are for the card view. No secret material is stored — the test LiteLLM
// key lives only in Vault KV, referenced by LLMKeyAlias.
type ProjectAgentTemplate struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Status  string `json:"status"` // draft | tested | published
	Version int    `json:"version"`

	YAMLSource  string `json:"yaml_source"`
	Description string `json:"description,omitempty"`
	Greeting    string `json:"greeting,omitempty"`
	Model       string `json:"model,omitempty"`
	// ToolSelection is server → selected tool names (empty slice = all the
	// server's tools), denormalized from the YAML for the card view.
	ToolSelection map[string][]string `json:"tool_selection,omitempty"`

	// Wiring records the ContextForge virtual servers + scoped-token names created
	// for this template, so publish reuses them and delete tears them down.
	Wiring []TemplateWiring `json:"wiring,omitempty"`

	TestResult  *AgentTestResult `json:"test_result,omitempty"`
	JobID       string           `json:"job_id,omitempty"`        // admin test job
	Endpoint    string           `json:"endpoint,omitempty"`      // host:port of the test job
	LLMKeyAlias string           `json:"llm_key_alias,omitempty"` // test LiteLLM key alias

	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TemplateWiring is one MCP server's ContextForge wiring for a template: the
// subset-scoped virtual server and the client token minted against it, plus the
// Vault KV path where the agent job reads that server's {url, token}.
type TemplateWiring struct {
	Server          string `json:"server"`
	VirtualServerID string `json:"virtual_server_id"`
	TokenName       string `json:"token_name"`
	KVPath          string `json:"kv_path"`
}

// ProjectAgentInstance is a project-user's isolated run of a published template.
// It owns a per-user Nomad job but reuses the template's LiteLLM key and scoped
// MCP token (per-user delegated identity is a later phase). Namespace is stored
// so the idle reaper can purge the job without a project lookup. No secret
// material is stored here.
type ProjectAgentInstance struct {
	Project      string    `json:"project"`
	Template     string    `json:"template"`
	Subject      string    `json:"subject"` // owner email
	Namespace    string    `json:"namespace"`
	Status       string    `json:"status"` // stopped | running
	JobID        string    `json:"job_id,omitempty"`
	Endpoint     string    `json:"endpoint,omitempty"` // host:port of the running job
	LastActiveAt time.Time `json:"last_active_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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

// ProjectCapabilities is a project's role→capability matrix row. Matrix maps a
// project role (project-admin / project-user) to the
// capabilities it grants (workspaces / ai-agents). A project without a row uses
// rbac.DefaultCapabilityMatrix; a stored matrix replaces the defaults wholesale.
// Get returns apperr.ErrNotFound when no row exists; Delete is idempotent (most
// projects never store a row). No secret material.
type ProjectCapabilities struct {
	Project   string              `json:"project"`
	Matrix    map[string][]string `json:"matrix"`
	CreatedBy string              `json:"created_by,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	UpdatedAt time.Time           `json:"updated_at"`
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
	Runtime         string    `json:"runtime,omitempty"`      // "", "nvidia", "kata" (informational)
	CodingAgent     string    `json:"coding_agent,omitempty"` // "claude" (default) | "bob"; selects how MCP is wired into the workspace
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

// CodingAgentSetting is the platform-admin allow-list state for one coding agent
// (the code-known set is {claude, bob}; only the enabled flag is persisted). An
// ABSENT row means enabled — so a fresh install offers every agent, and toggling
// one off writes a row with Enabled=false. No secret material.
type CodingAgentSetting struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// ProjectTemplate is a per-project "flavor": a snapshot of a base template's
// PublishedSource with the 10 project-static placeholders baked in (pass-1), the
// per-workspace ${...} tokens left for jobrender at launch. Created by a
// project-admin (Phase C) or backfilled from Vault KV. No secret material.
type ProjectTemplate struct {
	Project        string    `json:"project"`
	Flavor         string    `json:"flavor"`         // template key (defaults to the base name; may be custom)
	Base           string    `json:"base,omitempty"` // BaseJobTemplate.Name it derives from (needed for re-bake on edit)
	BaseVersion    int       `json:"base_version"`   // BaseJobTemplate.Version snapshotted
	Status         string    `json:"status"`
	RenderedSource string    `json:"rendered_source"`      // pass-1 baked + add-ons injected (what launch renders pass-2)
	BakedBase      string    `json:"baked_base,omitempty"` // pass-1 baked with @project-addons markers intact (snapshot; re-injected on add-on change)
	Label          string    `json:"label,omitempty"`
	Description    string    `json:"description,omitempty"`
	Image          string    `json:"image,omitempty"`
	GitRepoURL     string    `json:"git_repo_url,omitempty"`
	NodePool       string    `json:"node_pool,omitempty"`
	CodingAgent    string    `json:"coding_agent,omitempty"` // coding agent chosen at flavor create; drives agent wiring at inject time
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

// SharedVolume is a per-project shared EFS volume a project-admin created for
// package/build caches or datasets. It mounts into a project's workspaces at
// MountPath (read-only when ReadOnly); the portal provisions one EFS access point
// per volume via the Nomad CSI API. VolumeID is the Nomad CSI volume id
// (shared-<project>-<name>). No secret material is stored.
type SharedVolume struct {
	Project   string    `json:"project"`
	Name      string    `json:"name"`
	MountPath string    `json:"mount_path"`
	ReadOnly  bool      `json:"read_only"`
	VolumeID  string    `json:"volume_id"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the control-plane persistence the admin plane depends on.
type Store interface {
	// MCP server deploy definitions.

	// LLM model registry.
	UpsertLLMModel(ctx context.Context, m LLMModel) (LLMModel, error)
	GetLLMModel(ctx context.Context, name string) (LLMModel, error)
	ListLLMModels(ctx context.Context) ([]LLMModel, error)
	DeleteLLMModel(ctx context.Context, name string) error

	// Credential blueprints (platform-authored).

	// Project role elevations (group membership stays in IBM Verify).
	GrantProjectRole(ctx context.Context, pr ProjectRole) (ProjectRole, error)
	RevokeProjectRole(ctx context.Context, project, subject, role string) error
	HasProjectRole(ctx context.Context, project, subject, role string) (bool, error)
	ListProjectRoles(ctx context.Context, project string) ([]ProjectRole, error)
	ProjectRolesForSubject(ctx context.Context, subject string) ([]ProjectRole, error)

	// Per-project role→capability matrix (absent row = built-in defaults).
	UpsertProjectCapabilities(ctx context.Context, pc ProjectCapabilities) (ProjectCapabilities, error)
	GetProjectCapabilities(ctx context.Context, project string) (ProjectCapabilities, error)
	DeleteProjectCapabilities(ctx context.Context, project string) error

	// Project-deployed MCP servers (blueprint-instantiated).
	UpsertProjectMCPServer(ctx context.Context, s ProjectMCPServer) (ProjectMCPServer, error)
	GetProjectMCPServer(ctx context.Context, project, name string) (ProjectMCPServer, error)
	ListProjectMCPServers(ctx context.Context, project string) ([]ProjectMCPServer, error)
	DeleteProjectMCPServer(ctx context.Context, project, name string) error

	// Per-project shared volumes (EFS caches/datasets).
	CreateSharedVolume(ctx context.Context, v SharedVolume) (SharedVolume, error)
	GetSharedVolume(ctx context.Context, project, name string) (SharedVolume, error)
	ListSharedVolumes(ctx context.Context, project string) ([]SharedVolume, error)
	DeleteSharedVolume(ctx context.Context, project, name string) error

	// Project-deployed AI agents (YAML-configured deep agents).
	UpsertProjectAgent(ctx context.Context, a ProjectAgent) (ProjectAgent, error)
	GetProjectAgent(ctx context.Context, project, name string) (ProjectAgent, error)
	ListProjectAgents(ctx context.Context, project string) ([]ProjectAgent, error)
	DeleteProjectAgent(ctx context.Context, project, name string) error

	// Project-admin-authored AI agent templates (published as cards).
	UpsertProjectAgentTemplate(ctx context.Context, t ProjectAgentTemplate) (ProjectAgentTemplate, error)
	GetProjectAgentTemplate(ctx context.Context, project, name string) (ProjectAgentTemplate, error)
	ListProjectAgentTemplates(ctx context.Context, project string) ([]ProjectAgentTemplate, error)
	DeleteProjectAgentTemplate(ctx context.Context, project, name string) error

	// Per-user AI agent instances (a project-user's isolated run of a published
	// template). Keyed on (project, template, subject) so a user owns exactly one
	// instance per template and can never resolve another user's.
	UpsertProjectAgentInstance(ctx context.Context, in ProjectAgentInstance) (ProjectAgentInstance, error)
	GetProjectAgentInstance(ctx context.Context, project, template, subject string) (ProjectAgentInstance, error)
	ListProjectAgentInstancesForOwner(ctx context.Context, project, subject string) ([]ProjectAgentInstance, error)
	ListRunningProjectAgentInstances(ctx context.Context) ([]ProjectAgentInstance, error)
	CountProjectAgentInstances(ctx context.Context, project, template string) (int, error)
	DeleteProjectAgentInstance(ctx context.Context, project, template, subject string) error

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

	// Coding-agent allow-list (platform-admin toggles which agents are offered).
	// Absent row = enabled; a row is written only to disable/re-enable an agent.
	UpsertCodingAgentSetting(ctx context.Context, s CodingAgentSetting) error
	ListCodingAgentSettings(ctx context.Context) ([]CodingAgentSetting, error)

	// Project templates (per-project flavors; was Vault KV job-templates/<flavor>).
	UpsertProjectTemplate(ctx context.Context, t ProjectTemplate) (ProjectTemplate, error)
	GetProjectTemplate(ctx context.Context, project, flavor string) (ProjectTemplate, error)
	ListProjectTemplates(ctx context.Context, project string) ([]ProjectTemplate, error)
	DeleteProjectTemplate(ctx context.Context, project, flavor string) error

	// Admin audit log.
	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, limit int) ([]AuditEvent, error)
}
