// Package agents is the project-facing AI-agents plane. A project-admin authors an
// agent TEMPLATE in YAML (system prompt, model, a per-server subset of MCP tools,
// subagents), deploy-tests it, and publishes it as a card. A project-user then spins
// up an isolated per-user instance from a published card (see instances.go). This
// file holds the template lifecycle: author/deploy-test/test/publish/delete.
//
// A template's tool subset is enforced at the ContextForge gateway: each selected
// server gets a virtual server scoped to exactly the chosen tool ids and a scoped
// client token, brokered to the agent job over Vault KV — the token, not the runtime,
// bounds which tools the agent can call. The per-template test LiteLLM key + shared
// "agents" WIF role reach the agent the workspace way (Nomad template + WIF), never
// pasted into the job.
package agents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/agentjob"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Template lifecycle states.
const (
	statusDraft     = "draft"     // authored, may or may not have a live test job
	statusTested    = "tested"    // deploy-tested and /healthz passed
	statusPublished = "published" // visible to project-users as a card
)

// ProjectLookup resolves a project's descriptor (membership check + namespace).
type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

// NomadClient is the subset of the Nomad client the agents plane needs.
type NomadClient interface {
	RegisterJob(namespace, jobHCL, flavor string) (string, error)
	ResolvePlacement(namespace, jobID string) (ip string, port int, err error)
	PurgeJob(namespace, jobID string) error
	JobExists(namespace, jobID string) (bool, error)
}

// VaultAdmin is the subset of blueprint.VaultAdmin the agents plane needs to grant
// an agent read access to its LLM key + the project's MCP tokens (satisfied by the
// same *vaultAdmin the MCP plane uses).
type VaultAdmin interface {
	WritePolicy(ctx context.Context, ns, name, policyHCL string) error
	WriteWIFRole(ctx context.Context, ns, authPath, roleName string, role blueprint.WIFRole) error
	WriteKVv2(ctx context.Context, ns, mount, relPath string, data map[string]any) error
	DeleteKVv2Metadata(ctx context.Context, ns, mount, relPath string) error
}

// LLMKeyManager mints/deletes the per-agent LiteLLM virtual key.
type LLMKeyManager interface {
	GenerateKey(ctx context.Context, spec llmgw.KeySpec) (string, error)
	DeleteKeyByAlias(ctx context.Context, alias string) error
}

// HTTPDoer performs the chat/health requests to the agent container (faked in tests).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config holds the platform-wide values a deploy needs beyond the agent YAML.
type Config struct {
	NodePool                  string
	Image                     string   // agent-runtime container image
	LLMModels                 []string // served models the YAML's llm must be one of
	LLMMaxBudget              float64
	LLMRPMLimit               int
	LLMGatewayPrivateEndpoint string // base_url written into the agent's LLM KV
	MCPGatewayEndpoint        string // gateway base for the virtual-server URL
	MCPTokenDays              int    // scoped-token lifetime
	KVMount                   string // project KV mount, e.g. "secret"
	VaultAuthPath             string // WIF auth backend, e.g. "jwt-nomad"
	VaultBoundAudience        string // WIF bound audience
	VaultUserClaim            string // WIF user claim, e.g. "nomad_job_id"
}

func (c Config) withDefaults() Config {
	if c.KVMount == "" {
		c.KVMount = "secret"
	}
	if c.VaultAuthPath == "" {
		c.VaultAuthPath = "jwt-nomad"
	}
	if c.VaultUserClaim == "" {
		c.VaultUserClaim = "nomad_job_id"
	}
	if c.MCPTokenDays == 0 {
		c.MCPTokenDays = 30
	}
	return c
}

// Service authors, deploy-tests, tests, publishes, and deletes agent templates, and
// (instances.go) runs per-user instances of published templates.
type Service struct {
	store    store.Store
	projects ProjectLookup
	nomad    NomadClient
	vault    VaultAdmin
	llm      LLMKeyManager
	gateway  mcpgw.Client
	http     HTTPDoer
	cfg      Config
}

// New builds the agents service. A nil httpDoer defaults to an http.Client with no
// whole-request timeout (chat is a long-lived SSE stream; the portal WriteTimeout
// bounds it) but a bounded response-header timeout so a dead agent fails fast.
func New(st store.Store, projects ProjectLookup, nomad NomadClient, vault VaultAdmin, llm LLMKeyManager, gateway mcpgw.Client, httpDoer HTTPDoer, cfg Config) *Service {
	if httpDoer == nil {
		httpDoer = &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				ResponseHeaderTimeout: 15 * time.Second,
			},
		}
	}
	return &Service{store: st, projects: projects, nomad: nomad, vault: vault, llm: llm, gateway: gateway, http: httpDoer, cfg: cfg.withDefaults()}
}

// TemplateView is a stored template plus whether its admin test job is running.
type TemplateView struct {
	store.ProjectAgentTemplate
	Running bool `json:"running"`
}

// TemplateListResult is the templates-list payload.
type TemplateListResult struct {
	Templates []TemplateView `json:"templates"`
}

// ListTemplates returns the project's agent templates with live test-job status.
func (s *Service) ListTemplates(ctx context.Context, groups []string, project string) (TemplateListResult, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return TemplateListResult{}, err
	}
	rows, err := s.store.ListProjectAgentTemplates(ctx, project)
	if err != nil {
		return TemplateListResult{}, err
	}
	out := TemplateListResult{Templates: []TemplateView{}}
	for _, r := range rows {
		running := false
		if r.JobID != "" {
			running, _ = s.nomad.JobExists(d.Namespace, r.JobID)
		}
		out.Templates = append(out.Templates, TemplateView{ProjectAgentTemplate: r, Running: running})
	}
	return out, nil
}

// GetTemplate returns one template (membership-gated).
func (s *Service) GetTemplate(ctx context.Context, groups []string, project, name string) (store.ProjectAgentTemplate, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	return s.store.GetProjectAgentTemplate(ctx, project, name)
}

// ValidateYAML parses + validates template YAML against the project (served models,
// deployed MCP servers, and per-server tool subsets) without saving.
func (s *Service) ValidateYAML(ctx context.Context, groups []string, project, src string) error {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return err
	}
	spec, err := Parse(src)
	if err != nil {
		return err
	}
	modelKnown, serverTools := s.validators(ctx, project)
	return spec.Validate(modelKnown, serverTools)
}

// Save authors or edits a template as a draft — validate + persist, no deploy. A
// config change resets a tested/published template back to draft (it must be
// deploy-tested again before it can be re-published).
func (s *Service) Save(ctx context.Context, actor string, groups []string, project, src string) (store.ProjectAgentTemplate, error) {
	spec, err := s.prepare(ctx, groups, project, src)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	version := 1
	if existing, err := s.store.GetProjectAgentTemplate(ctx, project, spec.Name); err == nil {
		version = existing.Version + 1
	}
	row := store.ProjectAgentTemplate{
		Project: project, Name: spec.Name, Status: statusDraft, Version: version,
		YAMLSource: src, Description: spec.Description, Greeting: spec.Greeting, Model: spec.LLM,
		ToolSelection: spec.toolSelection(), CreatedBy: actor,
	}
	saved, err := s.store.UpsertProjectAgentTemplate(ctx, row)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	s.audit(ctx, actor, "project-agent-template.save", project+"/"+spec.Name, "ok", map[string]any{"version": version})
	return saved, nil
}

// prepare resolves the project (membership + namespace requirement), parses, and
// validates the YAML — the shared front half of Save/DeployTest.
func (s *Service) prepare(ctx context.Context, groups []string, project, src string) (Spec, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return Spec{}, err
	}
	if d.Namespace == "" {
		return Spec{}, fmt.Errorf("project %q has no namespace: %w", project, apperr.ErrBadRequest)
	}
	spec, err := Parse(src)
	if err != nil {
		return Spec{}, err
	}
	modelKnown, serverTools := s.validators(ctx, project)
	if err := spec.Validate(modelKnown, serverTools); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// DeployTest wires the template's tool subsets at the gateway, mints a test LiteLLM
// key, and runs the admin test job. It re-validates the saved YAML first. Any failure
// after the first gateway/key/KV mutation triggers a best-effort teardown so a
// project never accrues orphaned wiring.
func (s *Service) DeployTest(ctx context.Context, actor string, groups []string, project, name string) (store.ProjectAgentTemplate, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	if d.Namespace == "" {
		return store.ProjectAgentTemplate{}, fmt.Errorf("project %q has no namespace: %w", project, apperr.ErrBadRequest)
	}
	row, err := s.store.GetProjectAgentTemplate(ctx, project, name)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	spec, err := Parse(row.YAMLSource)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	modelKnown, serverTools := s.validators(ctx, project)
	if err := spec.Validate(modelKnown, serverTools); err != nil {
		return store.ProjectAgentTemplate{}, err
	}

	ns := d.Namespace
	target := project + "/" + name
	failStage := func(stage string, err error) (store.ProjectAgentTemplate, error) {
		s.audit(ctx, actor, "project-agent-template.deploy", target, "error", map[string]any{"stage": stage})
		return store.ProjectAgentTemplate{}, err
	}

	// Purge any prior test job before we churn the LLM key + KV below. Otherwise
	// the old alloc keeps serving during Nomad's rolling replace, but on the now-
	// deleted key — the intermittent chat 401 seen on redeploy. A fresh alloc
	// reads the newly-minted key from KV at startup.
	if row.JobID != "" {
		_ = s.nomad.PurgeJob(ns, row.JobID)
	}

	if err := s.ensureVaultAccess(ctx, ns, project); err != nil {
		return failStage("vault-access", err)
	}

	// Wire each selected server's tool subset at the gateway. Accumulate created
	// wiring so a later failure can tear down exactly what was made.
	wiring, err := s.wireTemplateServers(ctx, ns, project, name, spec.Tools.MCPServers)
	if err != nil {
		s.teardownWiring(ctx, ns, wiring)
		return failStage("wire-mcp", err)
	}
	cleanup := func() {
		s.teardownWiring(ctx, ns, wiring)
		_ = s.llm.DeleteKeyByAlias(ctx, templateKeyAlias(project, name))
		_ = s.vault.DeleteKVv2Metadata(ctx, ns, s.cfg.KVMount, kvRelPath(templateLLMLeaf(name)))
	}

	alias := templateKeyAlias(project, name)
	_ = s.llm.DeleteKeyByAlias(ctx, alias) // free a stale alias from a prior run
	key, err := s.llm.GenerateKey(ctx, llmgw.KeySpec{
		Alias: alias, Models: spec.models(),
		MaxBudget: s.cfg.LLMMaxBudget, RPMLimit: s.cfg.LLMRPMLimit,
		Metadata: map[string]string{"project": project, "template": name},
	})
	if err != nil {
		cleanup()
		return failStage("llm-key", err)
	}
	if err := s.vault.WriteKVv2(ctx, ns, s.cfg.KVMount, kvRelPath(templateLLMLeaf(name)), map[string]any{
		"base_url": s.cfg.LLMGatewayPrivateEndpoint, "virtual_key": key,
	}); err != nil {
		cleanup()
		return failStage("llm-kv", err)
	}

	hcl := agentjob.Render(agentjob.RenderSpec{
		JobName:        templateTestJob(project, name),
		Namespace:      ns,
		NodePool:       s.cfg.NodePool,
		Image:          s.cfg.Image,
		ServiceName:    templateTestJob(project, name),
		Tags:           templateTags(project, name),
		VaultNamespace: ns,
		VaultRole:      wifRole,
		AgentName:      templateLLMLeaf(name),
		MCPServers:     mcpRefs(wiring),
		AgentYAML:      row.YAMLSource,
	})
	jobID, err := s.nomad.RegisterJob(ns, hcl, "")
	if err != nil {
		cleanup()
		return failStage("register-job", err)
	}
	ip, port, err := s.nomad.ResolvePlacement(ns, jobID)
	if err == nil && port == 0 {
		err = fmt.Errorf("nomad: no http host port assigned for %q", jobID)
	}
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		cleanup()
		return failStage("resolve-placement", err)
	}

	// A config change may have moved the template off "tested"/"published"; a fresh
	// deploy always lands as draft until Test passes again.
	row.Status = statusDraft
	row.JobID = jobID
	row.Endpoint = endpoint(ip, port)
	row.LLMKeyAlias = alias
	row.Wiring = wiring
	row.TestResult = nil
	saved, err := s.store.UpsertProjectAgentTemplate(ctx, row)
	if err != nil {
		_ = s.nomad.PurgeJob(ns, jobID)
		cleanup()
		return failStage("persist", err)
	}
	s.audit(ctx, actor, "project-agent-template.deploy", target, "ok", nil)
	return saved, nil
}

// Test re-resolves the test job's placement and probes /healthz, recording the tool
// count. A pass advances the template to "tested" (eligible to publish); a config
// change since the last Test resets it to draft (via DeployTest), so tested always
// reflects the currently-deployed job.
func (s *Service) Test(ctx context.Context, actor string, groups []string, project, name string) (store.ProjectAgentTemplate, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	row, err := s.store.GetProjectAgentTemplate(ctx, project, name)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	if row.JobID == "" {
		return store.ProjectAgentTemplate{}, fmt.Errorf("template %q has not been deploy-tested yet: %w", name, apperr.ErrConflict)
	}
	if ip, port, rerr := s.nomad.ResolvePlacement(d.Namespace, row.JobID); rerr == nil && port > 0 {
		row.Endpoint = endpoint(ip, port)
	}
	row.TestResult = s.probeHealth(ctx, row.Endpoint)
	if row.TestResult.Passed && row.Status != statusPublished {
		row.Status = statusTested
	}
	saved, err := s.store.UpsertProjectAgentTemplate(ctx, row)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	outcome := "error"
	if row.TestResult.Passed {
		outcome = "ok"
	}
	s.audit(ctx, actor, "project-agent-template.test", project+"/"+name, outcome, nil)
	return saved, nil
}

// Publish flips a tested template to published, making it a card project-users can
// instantiate. The template must be in "tested" (deploy-tested + healthz-passed).
func (s *Service) Publish(ctx context.Context, actor string, groups []string, project, name string) (store.ProjectAgentTemplate, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	row, err := s.store.GetProjectAgentTemplate(ctx, project, name)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	if row.Status != statusTested {
		return store.ProjectAgentTemplate{}, fmt.Errorf("template %q must be deploy-tested before publishing: %w", name, apperr.ErrConflict)
	}
	// The template is proven, so retire the admin deploy-test job — per-user instances
	// carry the identity from here on. The LLM key, scoped MCP token, and KV persist
	// (instances reuse them); only the job goes. The admin re-runs Deploy-test to
	// chat-test again, which is why we also clear the stale endpoint.
	if row.JobID != "" {
		_ = s.nomad.PurgeJob(d.Namespace, row.JobID)
	}
	row.JobID = ""
	row.Endpoint = ""
	row.Status = statusPublished
	saved, err := s.store.UpsertProjectAgentTemplate(ctx, row)
	if err != nil {
		return store.ProjectAgentTemplate{}, err
	}
	s.audit(ctx, actor, "project-agent-template.publish", project+"/"+name, "ok", nil)
	return saved, nil
}

// ChatTest proxies one chat turn to the admin test job so a template can be exercised
// before publishing. Per-user delegated identity is added by the instance plane; the
// test job runs under the shared "agents" role, which is sufficient to verify tools.
func (s *Service) ChatTest(ctx context.Context, groups []string, project, name string, body io.Reader) (*http.Response, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return nil, err
	}
	row, err := s.store.GetProjectAgentTemplate(ctx, project, name)
	if err != nil {
		return nil, err
	}
	if row.Endpoint == "" {
		return nil, fmt.Errorf("template %q has no running test job yet: %w", name, apperr.ErrConflict)
	}
	payload, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read chat body: %w", apperr.ErrBadRequest)
	}
	resp, err := s.postChat(ctx, row.Endpoint, payload, nil)
	if err == nil {
		return resp, nil
	}
	ip, port, rerr := s.nomad.ResolvePlacement(d.Namespace, row.JobID)
	if rerr != nil || port == 0 {
		return nil, err
	}
	row.Endpoint = endpoint(ip, port)
	_, _ = s.store.UpsertProjectAgentTemplate(ctx, row)
	return s.postChat(ctx, row.Endpoint, payload, nil)
}

// DeleteTemplate purges the admin test job, tears down the gateway wiring + test
// LiteLLM key + KV, and drops the row. Blocked while any project-user still has an
// instance of the template (instances.go supplies the count).
func (s *Service) DeleteTemplate(ctx context.Context, actor string, groups []string, project, name string) error {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return err
	}
	row, err := s.store.GetProjectAgentTemplate(ctx, project, name)
	if err != nil {
		return err
	}
	if n, err := s.instanceCount(ctx, project, name); err == nil && n > 0 {
		return fmt.Errorf("template %q has %d running instance(s); delete those first: %w", name, n, apperr.ErrConflict)
	}
	if row.JobID != "" {
		_ = s.nomad.PurgeJob(d.Namespace, row.JobID)
	}
	s.teardownWiring(ctx, d.Namespace, row.Wiring)
	if row.LLMKeyAlias != "" {
		_ = s.llm.DeleteKeyByAlias(ctx, row.LLMKeyAlias)
	}
	_ = s.vault.DeleteKVv2Metadata(ctx, d.Namespace, s.cfg.KVMount, kvRelPath(templateLLMLeaf(name)))
	if err := s.store.DeleteProjectAgentTemplate(ctx, project, name); err != nil {
		return err
	}
	s.audit(ctx, actor, "project-agent-template.delete", project+"/"+name, "ok", nil)
	return nil
}

// wireTemplateServers creates, for each selected server, a virtual server scoped to
// the chosen tool subset (all the server's tools when none are named) + a scoped
// client token, and writes {url, token} to the template's per-server KV path.
func (s *Service) wireTemplateServers(ctx context.Context, ns, project, template string, sels []MCPServerSel) ([]store.TemplateWiring, error) {
	if s.gateway == nil && len(sels) > 0 {
		return nil, fmt.Errorf("MCP gateway not configured: %w", apperr.ErrBadRequest)
	}
	out := []store.TemplateWiring{}
	for _, sel := range sels {
		srv, err := s.store.GetProjectMCPServer(ctx, project, sel.Server)
		if err != nil {
			return out, err
		}
		if srv.PeerID == "" {
			return out, fmt.Errorf("mcp server %q must be tested before use in a template: %w", sel.Server, apperr.ErrConflict)
		}
		toolIDs, err := s.resolveToolIDs(ctx, srv.PeerID, sel.Tools)
		if err != nil {
			return out, err
		}
		vsName := templateVSName(project, template, sel.Server)
		vsID, err := s.gateway.CreateVirtualServer(ctx, vsName, "agent template "+template+" ("+sel.Server+")", toolIDs)
		if err != nil {
			return out, fmt.Errorf("create virtual server: %w", err)
		}
		tokenPrefix := vsName + "-client"
		if err := s.gateway.RevokeTokensByPrefix(ctx, tokenPrefix); err != nil {
			return out, fmt.Errorf("revoke stale tokens: %w", err)
		}
		suffix, err := randHex(4)
		if err != nil {
			return out, err
		}
		tokenName := tokenPrefix + "-" + suffix
		token, err := s.gateway.CreateScopedToken(ctx, tokenName, s.cfg.MCPTokenDays, vsID)
		if err != nil {
			return out, fmt.Errorf("create scoped token: %w", err)
		}
		url := strings.TrimRight(s.cfg.MCPGatewayEndpoint, "/") + "/servers/" + vsID + "/sse"
		kvRel := templateMCPKVRel(template, sel.Server)
		if err := s.vault.WriteKVv2(ctx, ns, s.cfg.KVMount, kvRel, map[string]any{"url": url, "token": token}); err != nil {
			return out, fmt.Errorf("write mcp kv: %w", err)
		}
		out = append(out, store.TemplateWiring{
			Server: sel.Server, VirtualServerID: vsID, TokenName: tokenName,
			KVPath: s.cfg.KVMount + "/data/" + kvRel,
		})
	}
	return out, nil
}

// teardownWiring best-effort removes the gateway virtual servers + client tokens and
// the per-server KV entries a template created.
func (s *Service) teardownWiring(ctx context.Context, ns string, wiring []store.TemplateWiring) {
	for _, w := range wiring {
		if s.gateway != nil {
			if w.VirtualServerID != "" {
				_ = s.gateway.DeleteVirtualServer(ctx, w.VirtualServerID)
			}
			if w.TokenName != "" {
				_ = s.gateway.RevokeTokensByPrefix(ctx, w.TokenName)
			}
		}
		_ = s.vault.DeleteKVv2Metadata(ctx, ns, s.cfg.KVMount, templateMCPKVRel2(w.KVPath, s.cfg.KVMount))
	}
}

// resolveToolIDs maps the selected tool NAMES to the peer's current gateway tool ids
// (an empty selection means every tool of the peer).
func (s *Service) resolveToolIDs(ctx context.Context, peerID string, selected []string) ([]string, error) {
	tools, err := s.gateway.ListTools(ctx, peerID)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	if len(selected) == 0 {
		ids := make([]string, 0, len(tools))
		for _, t := range tools {
			ids = append(ids, t.ID)
		}
		return ids, nil
	}
	byName := map[string]string{}
	for _, t := range tools {
		byName[t.Name] = t.ID
	}
	ids := make([]string, 0, len(selected))
	for _, name := range selected {
		id, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("tool %q is no longer exposed by the server: %w", name, apperr.ErrConflict)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ensureVaultAccess writes (idempotently) the per-project agents read policy and the
// shared "agents" WIF role bound to it. The policy grants read on the agent LLM keys +
// the project MCP tokens only — never GitHub/SSH-CA (workspace-only secrets).
func (s *Service) ensureVaultAccess(ctx context.Context, ns, project string) error {
	if err := s.vault.WritePolicy(ctx, ns, policyName(project), agentsReadPolicy); err != nil {
		return err
	}
	return s.vault.WriteWIFRole(ctx, ns, s.cfg.VaultAuthPath, wifRole, blueprint.WIFRole{
		BoundAudiences: []string{s.cfg.VaultBoundAudience},
		UserClaim:      s.cfg.VaultUserClaim,
		TokenPolicies:  []string{policyName(project)},
		TokenTTL:       "1800",
	})
}

// validators returns the model + tool-catalog checks Validate needs. serverTools
// returns a server's tool NAMES (from its cached catalog) and whether it is deployed.
func (s *Service) validators(ctx context.Context, project string) (modelKnown func(string) bool, serverTools func(string) ([]string, bool)) {
	models := map[string]bool{}
	for _, m := range s.cfg.LLMModels {
		models[m] = true
	}
	modelKnown = func(m string) bool { return models[m] }
	serverTools = func(name string) ([]string, bool) {
		srv, err := s.store.GetProjectMCPServer(ctx, project, name)
		if err != nil {
			return nil, false
		}
		// Prefer the live gateway catalog — the same source the authoring picker and
		// deploy-time resolveToolIDs use — so Save validates against exactly what the
		// admin selected. Fall back to the persisted catalog only if the gateway is
		// unreachable (stale/empty cache would otherwise reject valid selections).
		catalog := srv.Tools
		if live, lerr := s.gateway.ListTools(ctx, srv.PeerID); lerr == nil && len(live) > 0 {
			catalog = make([]store.MCPTool, 0, len(live))
			for _, t := range live {
				catalog = append(catalog, store.MCPTool{ID: t.ID, Name: t.Name, Description: t.Description})
			}
		}
		names := make([]string, 0, len(catalog))
		for _, t := range catalog {
			names = append(names, t.Name)
		}
		return names, true
	}
	return modelKnown, serverTools
}

func (s *Service) postChat(ctx context.Context, endpoint string, payload []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+endpoint+"/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return s.http.Do(req)
}

func (s *Service) probeHealth(ctx context.Context, endpoint string) *store.AgentTestResult {
	res := &store.AgentTestResult{At: time.Now()}
	if endpoint == "" {
		res.Message = "agent has no endpoint"
		return res
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/healthz", nil)
	resp, err := s.http.Do(req)
	if err != nil {
		res.Message = "healthz unreachable: " + err.Error()
		return res
	}
	defer resp.Body.Close()
	var hz struct {
		Status string   `json:"status"`
		Tools  []string `json:"tools"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&hz)
	if resp.StatusCode == http.StatusOK && hz.Status == "ok" {
		res.Passed = true
		res.ToolsDiscovered = len(hz.Tools)
		return res
	}
	res.Message = fmt.Sprintf("healthz status %d (%s)", resp.StatusCode, hz.Status)
	return res
}

// agentReadyTimeout bounds how long we wait for a freshly-placed agent container to
// start serving. A cold agent-runtime (image pull + uvicorn + MCP client/model build)
// observed ~50s; 90s leaves margin while staying under the 120s server WriteTimeout so
// the caller's first chat still has time to stream.
const agentReadyTimeout = 90 * time.Second

// waitAgentReady polls the agent's /healthz until it reports ready or the deadline
// passes. Returning before the container serves is the cold-start race that makes the
// first chat fail with connection-refused; a timeout is surfaced as a retryable 503.
func (s *Service) waitAgentReady(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(agentReadyTimeout)
	for {
		if s.probeHealth(ctx, endpoint).Passed {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("agent at %s is still starting, retry shortly: %w", endpoint, apperr.ErrUnavailable)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// instanceCount reports how many per-user instances of a template exist; a
// template cannot be deleted while any remain (their jobs read its Vault KV).
func (s *Service) instanceCount(ctx context.Context, project, template string) (int, error) {
	return s.store.CountProjectAgentInstances(ctx, project, template)
}

func (s *Service) audit(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: detail})
}

// models returns the distinct models the agent + its subagents declare (the LiteLLM
// key is scoped to exactly these).
func (s Spec) models() []string {
	seen := map[string]bool{s.LLM: true}
	out := []string{s.LLM}
	for _, sub := range s.Subagents {
		if sub.LLM != "" && !seen[sub.LLM] {
			seen[sub.LLM] = true
			out = append(out, sub.LLM)
		}
	}
	return out
}

// toolSelection flattens the parsed tool selection to server → selected tool names
// (empty slice = all tools) for the card view.
func (s Spec) toolSelection() map[string][]string {
	if len(s.Tools.MCPServers) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, sel := range s.Tools.MCPServers {
		out[sel.Server] = sel.Tools
	}
	return out
}

// mcpRefs turns the persisted wiring into the render-time MCP refs (name + KV path).
func mcpRefs(wiring []store.TemplateWiring) []agentjob.MCPRef {
	out := make([]agentjob.MCPRef, 0, len(wiring))
	for _, w := range wiring {
		out = append(out, agentjob.MCPRef{Name: w.Server, KVPath: w.KVPath})
	}
	return out
}

// wifRole is the per-project WIF role every agent job assumes; policyName is its
// bound read policy. Both are shared across a project's agents (no per-agent
// narrowing, same posture as the seeded workspace role).
const wifRole = "agents"

// agentsReadPolicy is the content of nomad-<project>-agents-read: read on the agent
// LLM keys + the project MCP tokens, and nothing else.
const agentsReadPolicy = `path "secret/data/projects/agents/*" { capabilities = ["read"] }
path "secret/data/projects/mcp" { capabilities = ["read"] }
path "secret/data/projects/mcp/*" { capabilities = ["read"] }
`

func policyName(project string) string { return "nomad-" + project + "-agents-read" }

// templateTestJob is the admin test job name; templateLLMLeaf is the LLM KV leaf
// (under projects/agents/); templateKeyAlias is the test LiteLLM alias.
func templateTestJob(project, template string) string {
	return "agent-" + project + "-tmpl-" + template
}
func templateLLMLeaf(template string) string { return "tmpl-" + template }
func templateKeyAlias(project, template string) string {
	return "llm-" + project + "-tmpl-" + template
}

// templateVSName is the ContextForge virtual-server name for one server of a template
// (distinct from the workspace wiring name "mcp-<project>-<server>").
func templateVSName(project, template, server string) string {
	return "tmpl-" + project + "-" + template + "-" + server
}

// templateMCPKVRel is the KV-v2 relative path (under the mount) for a template's
// per-server {url, token}; templateMCPKVRel2 recovers it from a stored full KVPath.
func templateMCPKVRel(template, server string) string {
	return "projects/agents/tmpl-" + template + "/mcp/" + server
}
func templateMCPKVRel2(fullKVPath, mount string) string {
	return strings.TrimPrefix(fullKVPath, mount+"/data/")
}

func kvRelPath(leaf string) string        { return "projects/agents/" + leaf }
func endpoint(ip string, port int) string { return fmt.Sprintf("%s:%d", ip, port) }

func templateTags(project, template string) []string {
	return []string{"agent-template", "agent.project=" + project, "agent.template=" + template}
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("agents: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
