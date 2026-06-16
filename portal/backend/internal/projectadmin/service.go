// Package projectadmin is the project-facing onboarding plane: a project-admin
// deploys a published, blueprint-backed MCP server type into their project. The
// deploy instantiates the credential blueprint into the project's Vault namespace
// and binds the minted WIF role into the Nomad job — credentials are
// blueprint-provisioned, never pasted. It mirrors internal/admin but targets the
// project namespace (descriptor.Namespace) and uses a WIF credential.
package projectadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

type Executor interface {
	Instantiate(ctx context.Context, m blueprint.BlueprintManifest, namespace string, params map[string]string) (blueprint.InstanceRecord, error)
	Deprovision(ctx context.Context, rec blueprint.InstanceRecord) error
}

type NomadClient interface {
	RegisterJob(namespace, jobHCL, flavor string) (string, error)
	ResolvePlacementIP(namespace, jobID string) (string, error)
	PurgeJob(namespace, jobID string) error
	JobExists(namespace, jobID string) (bool, error)
}

type VaultClient interface {
	ReadKVField(ctx context.Context, relPath, field string) (string, error)
}

type Config struct {
	BlueprintsKVPath string
	NodePool         string
}

func (c Config) withDefaults() Config {
	if c.BlueprintsKVPath == "" {
		c.BlueprintsKVPath = "infra/blueprints"
	}
	return c
}

type Service struct {
	store    store.Store
	projects ProjectLookup
	executor Executor
	nomad    NomadClient
	gateway  mcpgw.Client
	vault    VaultClient
	cfg      Config
}

func New(st store.Store, projects ProjectLookup, ex Executor, nomad NomadClient, gateway mcpgw.Client, vault VaultClient, cfg Config) *Service {
	return &Service{store: st, projects: projects, executor: ex, nomad: nomad, gateway: gateway, vault: vault, cfg: cfg.withDefaults()}
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

type DeployableType struct {
	Name      string                `json:"name"`
	Image     string                `json:"image"`
	Transport string                `json:"transport"`
	Params    []blueprint.ParamSpec `json:"params"`
}

type DeployedView struct {
	store.ProjectMCPServer
	Running bool `json:"running"`
}

type Catalog struct {
	Deployable []DeployableType `json:"deployable"`
	Deployed   []DeployedView   `json:"deployed"`
}

func (s *Service) ListDeployable(ctx context.Context, project string) (Catalog, error) {
	types, err := s.store.ListMCPServers(ctx)
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Deployable: []DeployableType{}, Deployed: []DeployedView{}}
	for _, t := range types {
		if t.Status != store.StatusPublished || t.BlueprintRef == nil {
			continue
		}
		m, err := s.loadManifest(ctx, *t.BlueprintRef)
		if err != nil {
			return Catalog{}, err
		}
		out.Deployable = append(out.Deployable, DeployableType{Name: t.Name, Image: t.Image, Transport: t.Transport, Params: m.Params})
	}
	rows, err := s.store.ListProjectMCPServers(ctx, project)
	if err != nil {
		return Catalog{}, err
	}
	for _, r := range rows {
		running := false
		if r.JobID != "" {
			running, _ = s.nomad.JobExists(s.projectNamespaceBestEffort(ctx, project), r.JobID)
		}
		out.Deployed = append(out.Deployed, DeployedView{ProjectMCPServer: r, Running: running})
	}
	return out, nil
}

func (s *Service) projectNamespaceBestEffort(ctx context.Context, project string) string {
	d, err := s.projects.GetProject(ctx, project, nil)
	if err != nil || d.Namespace == "" {
		return project
	}
	return d.Namespace
}

func (s *Service) loadManifest(ctx context.Context, ref store.BlueprintRef) (blueprint.BlueprintManifest, error) {
	bp, err := s.store.GetBlueprint(ctx, ref.ID, ref.Version)
	if err != nil {
		return blueprint.BlueprintManifest{}, fmt.Errorf("blueprint %s@%d: %w", ref.ID, ref.Version, apperr.ErrNotFound)
	}
	if bp.ContentHash != ref.ContentHash {
		return blueprint.BlueprintManifest{}, fmt.Errorf("blueprint ref hash drift: %w", apperr.ErrBadRequest)
	}
	raw, err := s.vault.ReadKVField(ctx, fmt.Sprintf("%s/%s/%d", s.cfg.BlueprintsKVPath, ref.ID, ref.Version), "manifest")
	if err != nil {
		return blueprint.BlueprintManifest{}, err
	}
	var m blueprint.BlueprintManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return blueprint.BlueprintManifest{}, fmt.Errorf("parse manifest: %w", apperr.ErrBadRequest)
	}
	if m.ContentHash() != ref.ContentHash {
		return blueprint.BlueprintManifest{}, fmt.Errorf("stored manifest hash does not match ref: %w", apperr.ErrBadRequest)
	}
	return m, nil
}

func (s *Service) audit(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: detail})
}
