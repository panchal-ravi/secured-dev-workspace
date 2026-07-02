// Package projecttemplate is the project-facing template plane: a project-admin
// creates a project "flavor" from a published base job template, supplying the
// image, git repo, and node pool. Creating a template performs the pass-1 bake —
// the 10 project-static placeholders are substituted from project-admin input plus
// values derived by convention from the project namespace — leaving the 5
// per-workspace ${...} tokens for jobrender at launch. The rendered source is
// snapshotted into Postgres so later edits to the base never disturb live projects.
package projecttemplate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
	"github.com/secured-dev-workspace/developer-portal/internal/jobtemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// ProjectLookup resolves the project descriptor + enforces membership (satisfied by
// *workspace.Service). The namespace it returns drives the pass-1 Vault-path bake.
type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

// Auditor records template mutations (satisfied by store.Store).
type Auditor interface {
	AppendAudit(ctx context.Context, ev store.AuditEvent) error
}

// EngineEnsurer idempotently mounts an extra add-on secret engine in the project's
// Vault namespace (satisfied by *projectengines.Service).
type EngineEnsurer interface {
	EnsureEngine(ctx context.Context, project, mount, engineType string) error
}

// MCPWirer makes a deployed MCP server consumable by the project's workspaces (mints a
// scoped gateway token + writes its {url,token} to the project MCP KV) so the injected
// template wiring resolves at launch (satisfied by *projectengines.Service). May be nil.
type MCPWirer interface {
	WireMCPServer(ctx context.Context, project, serverName string) error
}

// Config carries the infra coordinates the pass-1 bake needs. The model names come
// from terraform/infra (the LiteLLM model_list) so the base templates reference the
// governed models without hardcoding them.
type Config struct {
	LLMGatewayPrivateEndpoint string // baked as llm_base_url (Claude Code's ANTHROPIC_BASE_URL)
	LLMModelPrimary           string // baked as llm_model_primary (opus/sonnet slot)
	LLMModelFast              string // baked as llm_model_fast (haiku/subagent slot)
}

// Service creates/lists/deletes per-project templates and keeps the descriptor's
// Flavors[] in sync (workspace launch reads Flavors for node placement + the picker).
type Service struct {
	store    store.Store
	projects ProjectLookup
	audit    Auditor
	engines  EngineEnsurer // mounts extra add-on engines (may be nil)
	mcp      MCPWirer      // wires MCP servers into the workspace (may be nil)
	cfg      Config
}

// New builds the project-template service. engines/mcp may be nil (add-on wiring is
// then unavailable, e.g. in unit tests).
func New(st store.Store, projects ProjectLookup, audit Auditor, engines EngineEnsurer, mcp MCPWirer, cfg Config) *Service {
	return &Service{store: st, projects: projects, audit: audit, engines: engines, mcp: mcp, cfg: cfg}
}

// BaseOption is a published base template offered to a project-admin.
type BaseOption struct {
	Name            string          `json:"name"`
	Label           string          `json:"label,omitempty"`
	Description     string          `json:"description,omitempty"`
	Version         int             `json:"version"`
	Image           string          `json:"image,omitempty"` // baked into the project template; readonly to the project-admin
	DefaultNodePool string          `json:"default_node_pool,omitempty"`
	Runtime         string          `json:"runtime,omitempty"`
	Features        []store.Feature `json:"features,omitempty"`
}

// ListBase returns the published base templates a project-admin can build from.
// Membership on the project is enforced first.
func (s *Service) ListBase(ctx context.Context, groups []string, project string) ([]BaseOption, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return nil, err
	}
	bases, err := s.store.ListBaseJobTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BaseOption, 0, len(bases))
	for _, b := range bases {
		if b.Status != store.StatusPublished {
			continue
		}
		out = append(out, BaseOption{
			Name: b.Name, Label: b.Label, Description: b.Description, Version: b.Version,
			Image: b.Image, DefaultNodePool: b.DefaultNodePool, Runtime: b.Runtime, Features: b.Features,
		})
	}
	return out, nil
}

// List returns the project's existing templates.
func (s *Service) List(ctx context.Context, groups []string, project string) ([]store.ProjectTemplate, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return nil, err
	}
	return s.store.ListProjectTemplates(ctx, project)
}

// CreateInput is the project-admin's request. No secrets: GitHub App creds are
// supplied to the engine-provision plane, not here. The image is NOT accepted here
// — it is a property of the base template (portal-admin owned), baked from bt.Image.
type CreateInput struct {
	Base        string `json:"base"`             // published base template name
	Flavor      string `json:"flavor,omitempty"` // template key; defaults to Base
	GitRepoURL  string `json:"git_repo_url"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	NodePool    string `json:"node_pool,omitempty"`
}

// Create bakes a base template's PublishedSource into a project template (pass-1)
// and stores it, then re-syncs the descriptor's Flavors[].
func (s *Service) Create(ctx context.Context, actor string, groups []string, project string, in CreateInput) (store.ProjectTemplate, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return store.ProjectTemplate{}, err
	}
	base := strings.TrimSpace(in.Base)
	flavor := strings.TrimSpace(in.Flavor)
	if flavor == "" {
		flavor = base
	}
	if base == "" || strings.TrimSpace(in.GitRepoURL) == "" {
		return store.ProjectTemplate{}, fmt.Errorf("base and git_repo_url are required: %w", apperr.ErrBadRequest)
	}

	bt, err := s.store.GetBaseJobTemplate(ctx, base)
	if err != nil {
		return store.ProjectTemplate{}, err
	}
	if bt.Status != store.StatusPublished || bt.PublishedSource == "" {
		return store.ProjectTemplate{}, fmt.Errorf("base template %q is not published: %w", base, apperr.ErrBadRequest)
	}
	if strings.TrimSpace(bt.Image) == "" {
		return store.ProjectTemplate{}, fmt.Errorf("base template %q has no image set: %w", base, apperr.ErrBadRequest)
	}

	// Pass-1 bake (markers intact) is snapshotted as BakedBase; the launch-time
	// RenderedSource is that with the (initially empty) add-ons injected.
	bakedBase := jobrender.RenderPartial(bt.PublishedSource, projectStatic(project, s.cfg, in.GitRepoURL, bt.Image))
	rendered := jobtemplate.Inject(bakedBase, store.TemplateAddons{})
	// After pass-1 + inject the only tokens left must be the 5 per-workspace ones;
	// anything else would make jobrender.Render fail at launch.
	if err := onlyPerWorkspaceLeft(rendered); err != nil {
		return store.ProjectTemplate{}, err
	}

	label := in.Label
	if label == "" {
		label = bt.Label
	}
	nodePool := in.NodePool
	if nodePool == "" {
		nodePool = bt.DefaultNodePool
	}
	pt := store.ProjectTemplate{
		Project:        project,
		Flavor:         flavor,
		BaseVersion:    bt.Version,
		Status:         store.StatusReady,
		RenderedSource: rendered,
		BakedBase:      bakedBase,
		Label:          label,
		Description:    firstNonEmpty(in.Description, bt.Description),
		Image:          bt.Image,
		GitRepoURL:     in.GitRepoURL,
		NodePool:       nodePool,
		Features:       bt.Features,
		CreatedBy:      actor,
	}
	saved, err := s.store.UpsertProjectTemplate(ctx, pt)
	if err != nil {
		return store.ProjectTemplate{}, err
	}
	if err := s.syncDescriptorFlavors(ctx, project); err != nil {
		return store.ProjectTemplate{}, err
	}
	s.record(ctx, actor, project, "project-template.create", flavor, "ok", map[string]any{"base": base, "base_version": bt.Version})
	return saved, nil
}

// Delete removes a project template and re-syncs the descriptor's Flavors[].
func (s *Service) Delete(ctx context.Context, actor string, groups []string, project, flavor string) error {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return err
	}
	if err := s.store.DeleteProjectTemplate(ctx, project, flavor); err != nil {
		return err
	}
	if err := s.syncDescriptorFlavors(ctx, project); err != nil {
		return err
	}
	s.record(ctx, actor, project, "project-template.delete", flavor, "ok", nil)
	return nil
}

// SetAddons applies the project-admin's structured extension of a flavor: it mounts
// any declared extra secret engines and wires the selected MCP servers (side effects,
// via the engine plane), then re-injects the add-ons into the snapshotted BakedBase to
// produce a new RenderedSource. Idempotent-forward: the engine/MCP steps swallow
// already-exists, and the addon set is authoritative (a re-submit converges).
func (s *Service) SetAddons(ctx context.Context, actor string, groups []string, project, flavor string, addons store.TemplateAddons) (store.ProjectTemplate, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return store.ProjectTemplate{}, err
	}
	pt, err := s.store.GetProjectTemplate(ctx, project, flavor)
	if err != nil {
		return store.ProjectTemplate{}, err
	}
	if pt.BakedBase == "" {
		return store.ProjectTemplate{}, fmt.Errorf("template %q has no baked base to extend: %w", flavor, apperr.ErrBadRequest)
	}

	// Provision side effects first (so a workspace launched after this resolves them).
	for _, e := range addons.Engines {
		if s.engines == nil {
			return store.ProjectTemplate{}, fmt.Errorf("engine add-ons unavailable: %w", apperr.ErrBadRequest)
		}
		if err := s.engines.EnsureEngine(ctx, project, e.Mount, e.Type); err != nil {
			s.record(ctx, actor, project, "project-template.addons", flavor, "error", map[string]any{"engine": e.Mount})
			return store.ProjectTemplate{}, err
		}
	}
	for _, name := range addons.MCPServers {
		if s.mcp == nil {
			return store.ProjectTemplate{}, fmt.Errorf("MCP add-ons unavailable: %w", apperr.ErrBadRequest)
		}
		if err := s.mcp.WireMCPServer(ctx, project, name); err != nil {
			s.record(ctx, actor, project, "project-template.addons", flavor, "error", map[string]any{"mcp_server": name})
			return store.ProjectTemplate{}, err
		}
	}

	rendered := jobtemplate.Inject(pt.BakedBase, addons)
	if err := onlyPerWorkspaceLeft(rendered); err != nil {
		return store.ProjectTemplate{}, err
	}
	pt.Addons = addons
	pt.RenderedSource = rendered
	saved, err := s.store.UpsertProjectTemplate(ctx, pt)
	if err != nil {
		return store.ProjectTemplate{}, err
	}
	s.record(ctx, actor, project, "project-template.addons", flavor, "ok",
		map[string]any{"mcp_servers": len(addons.MCPServers), "engines": len(addons.Engines)})
	return saved, nil
}

// projectStatic derives the pass-1 values from the namespace (== project) by
// convention, plus the base template's image, the project-admin's repo, and the
// infra LLM endpoint + governed model names. mcp_kv_path is derived here too so it
// is available to injected add-ons (C-R.6), even though the base body no longer
// references it directly.
func projectStatic(project string, cfg Config, gitRepoURL, image string) map[string]string {
	return map[string]string{
		"namespace":         project,
		"wif_role":          project,
		"vault_namespace":   project,
		"image":             image,
		"git_repo_url":      gitRepoURL,
		"ssh_ca_path":       "ssh/config/ca",
		"github_token_path": "github/token/dev-workspace",
		"mcp_kv_path":       "secret/data/projects/mcp",
		"llm_kv_path":       "secret/data/projects/llm",
		"llm_base_url":      cfg.LLMGatewayPrivateEndpoint,
		"llm_model_primary": cfg.LLMModelPrimary,
		"llm_model_fast":    cfg.LLMModelFast,
	}
}

// onlyPerWorkspaceLeft asserts every remaining ${...} token is a per-workspace one.
func onlyPerWorkspaceLeft(rendered string) error {
	allowed := map[string]bool{}
	for _, k := range jobtemplate.PerWorkspacePlaceholders {
		allowed[k] = true
	}
	var bad []string
	for _, name := range jobrender.Placeholders(rendered) {
		if !allowed[name] {
			bad = append(bad, "${"+name+"}")
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: template has unbaked non-workspace placeholders: %s", apperr.ErrBadRequest, strings.Join(bad, ", "))
	}
	return nil
}

// syncDescriptorFlavors rebuilds the descriptor's Flavors[] from the project's
// template rows (workspace launch reads it for node placement + the picker),
// preserving every other descriptor field.
func (s *Service) syncDescriptorFlavors(ctx context.Context, project string) error {
	pd, err := s.store.GetProjectDescriptor(ctx, project)
	if err != nil {
		return err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return err
	}
	tmpls, err := s.store.ListProjectTemplates(ctx, project)
	if err != nil {
		return err
	}
	flavors := make([]descriptor.Flavor, 0, len(tmpls))
	for _, t := range tmpls {
		flavors = append(flavors, descriptor.Flavor{
			Name: t.Flavor, Label: t.Label, Description: t.Description,
			GitRepoURL: t.GitRepoURL, Image: t.Image, NodePool: t.NodePool,
			Features: toDescriptorFeatures(t.Features),
		})
	}
	d.Flavors = flavors
	js, err := json.Marshal(d)
	if err != nil {
		return err
	}
	pd.Descriptor = js
	_, err = s.store.UpsertProjectDescriptor(ctx, pd)
	return err
}

func toDescriptorFeatures(in []store.Feature) []descriptor.Feature {
	out := make([]descriptor.Feature, 0, len(in))
	for _, f := range in {
		out = append(out, descriptor.Feature{Key: f.Key, Label: f.Label, Description: f.Description})
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func (s *Service) record(ctx context.Context, actor, project, action, flavor, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: project + "/" + flavor, Outcome: outcome, Detail: detail})
}
