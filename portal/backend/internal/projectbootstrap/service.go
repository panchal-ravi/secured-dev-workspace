package projectbootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/rbac"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// projectNameRE mirrors terraform/project variables.tf: the name is reused as the
// Vault namespace, Nomad namespace, Boundary scope, and WIF role, so it must be a
// safe, lowercase, DNS-ish label.
var projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

// boundaryHostCatalogName is the fixed name of the per-project shared static host
// catalog the portal creates at project bootstrap. Per-workspace host-sets/targets
// live in it; the external Nomad→Boundary host-sync looks it up by this name in each
// project scope to keep workspace host addresses current.
const boundaryHostCatalogName = "dev-workspaces"

// NomadNSClient is the Nomad control-plane surface a project create/delete needs
// (satisfied by *hashistack.Nomad).
type NomadNSClient interface {
	CreateNamespace(name, description string) error
	UpsertACLPolicy(name, description, rulesHCL string) error
	CreateBindingRule(authMethod, selector, bindName string) error
	ListJobIDs(namespace string) ([]string, error)
	PurgeJob(namespace, jobID string) error
	ListCSIVolumeNames(namespace string) ([]string, error)
	DeleteHostVolume(namespace, name string) error
	DeleteNamespace(name string) error
	DeleteACLPolicy(name string) error
	DeleteBindingRulesForPolicy(bindName string) error
}

// BoundaryScopeClient creates/deletes the project scope (satisfied by
// *hashistack.Boundary). Scope deletion is recursive.
type BoundaryScopeClient interface {
	CreateProjectScope(ctx context.Context, orgScopeID, name, description string) (string, error)
	CreateHostCatalog(ctx context.Context, scopeID, name string) (string, error)
	DeleteScope(ctx context.Context, scopeID string) error
}

// GatewayCleaner is the best-effort ContextForge teardown surface (satisfied by
// mcpgw.Client). May be nil (gateway plane disabled) — cleanup is then skipped.
type GatewayCleaner interface {
	DeletePeer(ctx context.Context, peerID string) error
	RevokeTokensByPrefix(ctx context.Context, prefix string) error
}

// LLMKeyDeleter frees the project's LiteLLM virtual key on delete (satisfied by
// llmgw.Client). May be nil.
type LLMKeyDeleter interface {
	DeleteKeyByAlias(ctx context.Context, alias string) error
}

// DescriptorStore persists the project descriptor to the Postgres control-plane
// store (satisfied by store.Store). The descriptor was previously written to Vault
// KV; portal control-plane state now lives in Postgres.
type DescriptorStore interface {
	UpsertProjectDescriptor(ctx context.Context, d store.ProjectDescriptor) (store.ProjectDescriptor, error)
	GetProjectDescriptor(ctx context.Context, project string) (store.ProjectDescriptor, error)
	DeleteProjectDescriptor(ctx context.Context, project string) error
	ListProjectMCPServers(ctx context.Context, project string) ([]store.ProjectMCPServer, error)
	DeleteProjectMCPServer(ctx context.Context, project, name string) error
	ListProjectTemplates(ctx context.Context, project string) ([]store.ProjectTemplate, error)
	DeleteProjectTemplate(ctx context.Context, project, flavor string) error
	ListProjectRoles(ctx context.Context, project string) ([]store.ProjectRole, error)
	RevokeProjectRole(ctx context.Context, project, subject, role string) error
	DeleteProjectCapabilities(ctx context.Context, project string) error
}

// RoleGranter bootstraps the first project-admin (satisfied by *projectrole.Service).
type RoleGranter interface {
	Grant(ctx context.Context, actor, project, subject, role string) (store.ProjectRole, error)
}

// EngineProvisioner stands up the project's standard secret engines (SSH + GitHub
// mount + LLM virtual key + Boundary) at create time, with GitHub App config left
// empty for the project-admin to supply later (satisfied by *projectengines.Service).
type EngineProvisioner interface {
	ProvisionAtCreate(ctx context.Context, actor, project string) (descriptor.Descriptor, error)
}

// Auditor records create-project events (satisfied by store.Store).
type Auditor interface {
	AppendAudit(ctx context.Context, ev store.AuditEvent) error
}

// Config carries the foundation coordinates the shell + descriptor need.
type Config struct {
	NomadOIDCAuthMethod      string // Nomad OIDC auth method the binding rule attaches to
	BoundaryOrgScopeID       string // parent org scope for project scopes
	BoundaryOIDCAuthMethodID string // recorded in the descriptor
	InstancePrivateIP        string // recorded in the descriptor (all-in-one node)
}

// Service creates a project's Tier-1 shell across Vault, Nomad, and Boundary, then
// writes the descriptor and bootstraps the first project-admin. Every step is
// idempotent so a re-run after a transient failure converges; steps run in
// dependency order and a failure returns a typed, step-named error. (Compensating
// rollback of a partially-created project is a documented follow-up — re-run is the
// recovery path today.)
type Service struct {
	vc       *VaultCreator
	nomad    NomadNSClient
	boundary BoundaryScopeClient
	desc     DescriptorStore
	roles    RoleGranter
	engines  EngineProvisioner
	gateway  GatewayCleaner // best-effort ContextForge cleanup on delete (may be nil)
	llm      LLMKeyDeleter  // frees the project's LiteLLM virtual key on delete (may be nil)
	audit    Auditor
	cfg      Config
}

// NewService builds the project-create orchestration service. engines/gateway/llm
// may be nil (auto-provision at create resp. delete-time gateway/LLM cleanup are
// then skipped — e.g. in unit tests or with the admin plane off).
func NewService(vc *VaultCreator, nomad NomadNSClient, boundary BoundaryScopeClient, desc DescriptorStore, roles RoleGranter, engines EngineProvisioner, gateway GatewayCleaner, llm LLMKeyDeleter, audit Auditor, cfg Config) *Service {
	return &Service{vc: vc, nomad: nomad, boundary: boundary, desc: desc, roles: roles, engines: engines, gateway: gateway, llm: llm, audit: audit, cfg: cfg}
}

// CreateProjectInput is the platform-admin's request. Flavors/templates are a
// Phase-3 concern (engines + job-templates); a freshly created shell has no
// launchable flavors until those are added.
type CreateProjectInput struct {
	ProjectName         string `json:"project_name"`
	DevelopersGroupName string `json:"developers_group_name"`
	WorkspaceUser       string `json:"workspace_user"`
	FirstAdmin          string `json:"first_admin"`
}

// CreateProject provisions the shell and returns the written descriptor.
func (s *Service) CreateProject(ctx context.Context, actor string, in CreateProjectInput) (descriptor.Descriptor, error) {
	p := strings.TrimSpace(in.ProjectName)
	group := strings.TrimSpace(in.DevelopersGroupName)
	admin := strings.ToLower(strings.TrimSpace(in.FirstAdmin))
	wsUser := strings.TrimSpace(in.WorkspaceUser)
	if wsUser == "" {
		wsUser = "dev"
	}
	if !projectNameRE.MatchString(p) {
		return descriptor.Descriptor{}, fmt.Errorf("project_name must be a lowercase alphanumeric/hyphen label (1-63 chars): %w", apperr.ErrBadRequest)
	}
	if group == "" || admin == "" {
		return descriptor.Descriptor{}, fmt.Errorf("developers_group_name and first_admin are required: %w", apperr.ErrBadRequest)
	}
	if !s.vc.Enabled() {
		return descriptor.Descriptor{}, fmt.Errorf("project-create plane disabled (creator broker not configured): %w", apperr.ErrBadRequest)
	}

	// 1. Vault: child namespace + auth + provisioner/workspace roles.
	if err := s.vc.CreateNamespace(ctx, p); err != nil {
		return s.failf(ctx, actor, p, "vault.namespace", err)
	}
	if err := s.vc.EnableJWTNomad(ctx, p); err != nil {
		return s.failf(ctx, actor, p, "vault.auth", err)
	}
	if err := s.vc.MountSecretKV(ctx, p); err != nil {
		return s.failf(ctx, actor, p, "vault.kv", err)
	}
	if err := s.vc.SeedProvisioner(ctx, p); err != nil {
		return s.failf(ctx, actor, p, "vault.provisioner", err)
	}
	if err := s.vc.SeedWorkspaceRole(ctx, p, p); err != nil {
		return s.failf(ctx, actor, p, "vault.workspace-role", err)
	}

	// 2. Nomad: namespace + dev policy + OIDC binding rule.
	if err := s.nomad.CreateNamespace(p, "Project namespace for "+p); err != nil {
		return s.failf(ctx, actor, p, "nomad.namespace", err)
	}
	policyName := "project-" + p + "-dev"
	rules := fmt.Sprintf("namespace %q {\n  policy = \"write\"\n}\n", p)
	if err := s.nomad.UpsertACLPolicy(policyName, "Write within the "+p+" namespace only ("+group+")", rules); err != nil {
		return s.failf(ctx, actor, p, "nomad.policy", err)
	}
	selector := fmt.Sprintf("%q in list.groups", group)
	if err := s.nomad.CreateBindingRule(s.cfg.NomadOIDCAuthMethod, selector, policyName); err != nil {
		return s.failf(ctx, actor, p, "nomad.binding-rule", err)
	}

	// 3. Boundary: project scope + the project's shared host catalog (one per project;
	// per-workspace host-sets/targets live in it, the external host-sync fills the hosts).
	scopeID, err := s.boundary.CreateProjectScope(ctx, s.cfg.BoundaryOrgScopeID, p, "Project scope for "+p)
	if err != nil {
		return s.failf(ctx, actor, p, "boundary.scope", err)
	}
	catalogID, err := s.boundary.CreateHostCatalog(ctx, scopeID, boundaryHostCatalogName)
	if err != nil {
		return s.failf(ctx, actor, p, "boundary.host-catalog", err)
	}

	// 4. Descriptor (Postgres control-plane store). credential_library_id + flavors
	// are filled by the Phase-3 engines/templates; the shell records the connection
	// coordinates.
	d := descriptor.Descriptor{
		ProjectName:              p,
		Namespace:                p,
		ProjectScopeID:           scopeID,
		BoundaryHostCatalogID:    catalogID,
		DevelopersGroupName:      group,
		BoundaryOIDCAuthMethodID: s.cfg.BoundaryOIDCAuthMethodID,
		InstancePrivateIP:        s.cfg.InstancePrivateIP,
		WorkspaceUser:            wsUser,
		AliasSuffix:              "boundary",
		Flavors:                  []descriptor.Flavor{},
	}
	js, err := json.Marshal(d)
	if err != nil {
		return s.failf(ctx, actor, p, "descriptor.marshal", err)
	}
	if _, err := s.desc.UpsertProjectDescriptor(ctx, store.ProjectDescriptor{
		Project:    p,
		Status:     store.StatusReady,
		Descriptor: js,
		CreatedBy:  actor,
	}); err != nil {
		return s.failf(ctx, actor, p, "descriptor.write", err)
	}

	// 5. Bootstrap the first project-admin.
	if _, err := s.roles.Grant(ctx, actor, p, admin, string(rbac.RoleProjectAdmin)); err != nil {
		return s.failf(ctx, actor, p, "role.grant", err)
	}

	// 6. Auto-provision the standard secret engines (SSH + GitHub mount + LLM virtual
	// key + Boundary cred-store/library) with EMPTY GitHub App config. The project-admin
	// supplies the GitHub creds later via the Engines page. Idempotent-forward: an engine
	// failure marks the descriptor errored and a re-run converges. Returns the updated
	// descriptor (credential_library_id filled).
	if s.engines != nil {
		provisioned, err := s.engines.ProvisionAtCreate(ctx, actor, p)
		if err != nil {
			return s.failf(ctx, actor, p, "engines.provision", err)
		}
		d = provisioned
	}

	s.record(ctx, actor, p, "ok", map[string]any{"first_admin": admin, "developers_group": group})
	return d, nil
}

// ProjectDetail is the platform-admin's full view of a project: the descriptor plus
// the store row's lifecycle metadata.
type ProjectDetail struct {
	descriptor.Descriptor
	Status    string `json:"status"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// GetProject returns a project's descriptor + row metadata (platform-admin plane —
// no group filter; the mux gates on platform-admin).
func (s *Service) GetProject(ctx context.Context, project string) (ProjectDetail, error) {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return ProjectDetail{}, err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return ProjectDetail{}, err
	}
	return ProjectDetail{
		Descriptor: d,
		Status:     pd.Status,
		CreatedBy:  pd.CreatedBy,
		CreatedAt:  pd.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  pd.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// UpdateProjectInput is the platform-admin's edit of a project. Only the developers
// group is editable: the name/namespace key Vault, Nomad and Boundary resources and
// the workspace user is baked into the provisioned SSH role + Boundary library, so
// both are immutable here.
type UpdateProjectInput struct {
	DevelopersGroupName string `json:"developers_group_name"`
}

// UpdateProject edits the descriptor's developers group. Takes effect on the next
// request (portal membership is checked live); running workspaces are untouched.
// Note the Nomad UI OIDC binding rule created at project-create still references
// the original group — direct Nomad UI access follows the old group until that rule
// is updated out-of-band.
func (s *Service) UpdateProject(ctx context.Context, actor, project string, in UpdateProjectInput) (ProjectDetail, error) {
	group := strings.TrimSpace(in.DevelopersGroupName)
	if group == "" {
		return ProjectDetail{}, fmt.Errorf("developers_group_name is required: %w", apperr.ErrBadRequest)
	}
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return ProjectDetail{}, err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return ProjectDetail{}, err
	}
	d.DevelopersGroupName = group
	js, err := json.Marshal(d)
	if err != nil {
		return ProjectDetail{}, err
	}
	pd.Descriptor = js
	saved, err := s.desc.UpsertProjectDescriptor(ctx, pd)
	if err != nil {
		return ProjectDetail{}, err
	}
	if s.audit != nil {
		_ = s.audit.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: "project.update", Target: project, Outcome: "ok", Detail: map[string]any{"developers_group": group}})
	}
	return ProjectDetail{
		Descriptor: d,
		Status:     saved.Status,
		CreatedBy:  saved.CreatedBy,
		CreatedAt:  saved.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  saved.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// DeleteProject tears a project down across every plane — the inverse of
// CreateProject plus everything the engine/MCP/template planes added since.
// Steps are best-effort and idempotent: failures are collected, and on any
// failure the descriptor row is KEPT with status "error" so the project stays
// visible and the delete can be retried to convergence. The row is removed only
// after every plane confirmed. Known residual: virtual servers ContextForge
// created for template wiring are reused-by-name, so a leftover one is harmless.
func (s *Service) DeleteProject(ctx context.Context, actor, project string) error {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return err
	}
	ns := d.Namespace
	if ns == "" {
		ns = project
	}

	var errs []string
	fail := func(step string, err error) { errs = append(errs, step+": "+err.Error()) }

	// 1. Deployed MCP servers: purge jobs, drop gateway peers + scoped client
	// tokens, delete rows. Vault-side blueprint objects die with the namespace (4).
	rows, err := s.desc.ListProjectMCPServers(ctx, project)
	if err != nil {
		fail("store.list-mcp", err)
	}
	for _, r := range rows {
		if r.JobID != "" {
			_ = s.nomad.PurgeJob(ns, r.JobID)
		}
		if s.gateway != nil {
			if r.PeerID != "" {
				_ = s.gateway.DeletePeer(ctx, r.PeerID)
			}
			_ = s.gateway.RevokeTokensByPrefix(ctx, project+"-"+r.Name)
		}
		if err := s.desc.DeleteProjectMCPServer(ctx, project, r.Name); err != nil {
			fail("store.mcp "+r.Name, err)
		}
	}

	// 2. Nomad: purge every remaining job (workspaces), then namespace, binding
	// rules, ACL policy.
	if ids, err := s.nomad.ListJobIDs(ns); err != nil {
		fail("nomad.list-jobs", err)
	} else {
		for _, id := range ids {
			if err := s.nomad.PurgeJob(ns, id); err != nil {
				fail("nomad.purge "+id, err)
			}
		}
	}
	// Sweep dynamic host volumes (workspace homes) BEFORE the namespace: Nomad
	// deletes a namespace that still holds volumes, leaving them as undeletable
	// orphans — and the home data would silently survive on the node's disk.
	if vols, err := s.nomad.ListCSIVolumeNames(ns); err != nil {
		fail("nomad.list-volumes", err)
	} else {
		for _, v := range vols {
			if err := s.nomad.DeleteHostVolume(ns, v); err != nil {
				fail("nomad.volume "+v, err)
			}
		}
	}
	if err := s.nomad.DeleteNamespace(ns); err != nil {
		fail("nomad.namespace", err)
	}
	policyName := "project-" + project + "-dev"
	if err := s.nomad.DeleteBindingRulesForPolicy(policyName); err != nil {
		fail("nomad.binding-rule", err)
	}
	if err := s.nomad.DeleteACLPolicy(policyName); err != nil {
		fail("nomad.policy", err)
	}

	// 3. Boundary: recursive scope delete (targets, cred stores/libraries, roles).
	if d.ProjectScopeID != "" {
		if err := s.boundary.DeleteScope(ctx, d.ProjectScopeID); err != nil {
			fail("boundary.scope", err)
		}
	}

	// 4. Vault: delete the child namespace via the creator broker — Vault queues
	// removal of everything inside (mounts, auth, policies, leases).
	if s.vc != nil {
		if err := s.vc.DeleteNamespace(ctx, ns); err != nil {
			fail("vault.namespace", err)
		}
	}

	// 5. LiteLLM virtual key (best-effort; alias mirrors projectengines).
	if s.llm != nil {
		_ = s.llm.DeleteKeyByAlias(ctx, "llm-"+project)
	}

	// 6. Store rows — descriptor LAST so a partial delete stays visible/retryable.
	if tmpls, err := s.desc.ListProjectTemplates(ctx, project); err != nil {
		fail("store.list-templates", err)
	} else {
		for _, t := range tmpls {
			if err := s.desc.DeleteProjectTemplate(ctx, project, t.Flavor); err != nil {
				fail("store.template "+t.Flavor, err)
			}
		}
	}
	if prs, err := s.desc.ListProjectRoles(ctx, project); err != nil {
		fail("store.list-roles", err)
	} else {
		for _, r := range prs {
			if err := s.desc.RevokeProjectRole(ctx, project, r.Subject, r.Role); err != nil {
				fail("store.role "+r.Subject, err)
			}
		}
	}
	// Capability matrix row (idempotent; most projects never store one).
	if err := s.desc.DeleteProjectCapabilities(ctx, project); err != nil {
		fail("store.capabilities", err)
	}

	if len(errs) > 0 {
		pd.Status = store.StatusError
		if _, uerr := s.desc.UpsertProjectDescriptor(ctx, pd); uerr != nil {
			errs = append(errs, "descriptor.mark-error: "+uerr.Error())
		}
		s.recordDelete(ctx, actor, project, "error", map[string]any{"errors": errs})
		return fmt.Errorf("project delete incomplete (retry converges): %s", strings.Join(errs, "; "))
	}
	if err := s.desc.DeleteProjectDescriptor(ctx, project); err != nil {
		return err
	}
	s.recordDelete(ctx, actor, project, "ok", nil)
	return nil
}

func (s *Service) recordDelete(ctx context.Context, actor, project, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: "project.delete", Target: project, Outcome: outcome, Detail: detail})
}

// failf audits the failed step and returns the wrapped error.
func (s *Service) failf(ctx context.Context, actor, project, step string, err error) (descriptor.Descriptor, error) {
	s.record(ctx, actor, project, "error", map[string]any{"step": step, "error": err.Error()})
	return descriptor.Descriptor{}, fmt.Errorf("project create failed at %s: %w", step, err)
}

// record is a best-effort audit write (mirrors the other planes' handling).
func (s *Service) record(ctx context.Context, actor, project, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: "project.create", Target: project, Outcome: outcome, Detail: detail})
}
