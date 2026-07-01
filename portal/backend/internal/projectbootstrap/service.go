package projectbootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/rbac"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// projectNameRE mirrors terraform/project variables.tf: the name is reused as the
// Vault namespace, Nomad namespace, Boundary scope, and WIF role, so it must be a
// safe, lowercase, DNS-ish label.
var projectNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`)

// NomadNSClient is the Nomad control-plane surface a project create needs
// (satisfied by *hashistack.Nomad).
type NomadNSClient interface {
	CreateNamespace(name, description string) error
	UpsertACLPolicy(name, description, rulesHCL string) error
	CreateBindingRule(authMethod, selector, bindName string) error
}

// BoundaryScopeClient creates the project scope (satisfied by *hashistack.Boundary).
type BoundaryScopeClient interface {
	CreateProjectScope(ctx context.Context, orgScopeID, name, description string) (string, error)
}

// DescriptorWriter writes the project descriptor to the root control-plane KV
// (satisfied by *hashistack.Vault).
type DescriptorWriter interface {
	WriteKV(ctx context.Context, relPath string, data map[string]any) error
}

// RoleGranter bootstraps the first project-admin (satisfied by *projectrole.Service).
type RoleGranter interface {
	Grant(ctx context.Context, actor, project, subject, role string) (store.ProjectRole, error)
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
	kv       DescriptorWriter
	roles    RoleGranter
	audit    Auditor
	cfg      Config
}

// NewService builds the project-create orchestration service.
func NewService(vc *VaultCreator, nomad NomadNSClient, boundary BoundaryScopeClient, kv DescriptorWriter, roles RoleGranter, audit Auditor, cfg Config) *Service {
	return &Service{vc: vc, nomad: nomad, boundary: boundary, kv: kv, roles: roles, audit: audit, cfg: cfg}
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

	// 3. Boundary: project scope.
	scopeID, err := s.boundary.CreateProjectScope(ctx, s.cfg.BoundaryOrgScopeID, p, "Project scope for "+p)
	if err != nil {
		return s.failf(ctx, actor, p, "boundary.scope", err)
	}

	// 4. Descriptor (root KV). credential_library_id + flavors are filled by the
	// Phase-3 engines/templates; the shell records the connection coordinates.
	d := descriptor.Descriptor{
		ProjectName:              p,
		Namespace:                p,
		ProjectScopeID:           scopeID,
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
	if err := s.kv.WriteKV(ctx, "projects/"+p+"/portal-descriptor", map[string]any{"descriptor": string(js)}); err != nil {
		return s.failf(ctx, actor, p, "descriptor.write", err)
	}

	// 5. Bootstrap the first project-admin.
	if _, err := s.roles.Grant(ctx, actor, p, admin, string(rbac.RoleProjectAdmin)); err != nil {
		return s.failf(ctx, actor, p, "role.grant", err)
	}

	s.record(ctx, actor, p, "ok", map[string]any{"first_admin": admin, "developers_group": group})
	return d, nil
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
