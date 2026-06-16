// Package admin is the Platform Admin onboarding plane: it deploys existing MCP
// servers as Nomad jobs and onboards LLM models into the LiteLLM gateway, and
// verifies each the way a Project Admin will consume it (a ContextForge virtual
// server + scoped token for MCP; a scoped budgeted/rate-limited key for LLM).
//
// All onboarding logic lives in Service (API-first): the HTTP handlers are thin
// adapters over these methods, so the same operations can back a programmatic
// onboarding API later. Every mutation writes an audit event.
package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// blueprintValidator gates blueprint publishing (satisfied by *blueprint.Validator).
type blueprintValidator interface {
	Validate(ctx context.Context, m blueprint.BlueprintManifest) (blueprint.ValidationResult, error)
}

// NomadClient is the subset of the Nomad API the admin plane uses (satisfied by
// *hashistack.Nomad). Defined here so the service can be tested with a fake.
type NomadClient interface {
	RegisterJob(namespace, jobHCL, flavor string) (string, error)
	ResolvePlacementIP(namespace, jobID string) (string, error)
	PurgeJob(namespace, jobID string) error
	UsedPorts() ([]int, error)
}

// VaultClient is the KV access the admin plane needs (satisfied by *hashistack.Vault).
type VaultClient interface {
	// ReadKVField reads a single field from a KV-v2 secret at relPath.
	ReadKVField(ctx context.Context, relPath, field string) (string, error)
	// WriteKV writes data to a KV-v2 secret at relPath.
	WriteKV(ctx context.Context, relPath string, data map[string]any) error
}

// Config tunes the onboarding plane. Namespaces/pools/paths are platform-wide.
type Config struct {
	MCPNamespace       string // namespace for deployed MCP servers (e.g. "infra-mcp")
	NodePool           string // node pool for MCP jobs (e.g. "agents"; "" = default)
	MCPJobVaultRole    string // WIF role stamped on MCP jobs that reference secrets
	MCPServersKVPath   string // KV path prefix for published descriptors (e.g. "infra/mcp-servers")
	LLMProvidersKVPath string // KV path prefix for provider keys (e.g. "infra/llm-providers")
	BlueprintsKVPath   string // KV path prefix for canonical manifests (e.g. "infra/blueprints")
}

func (c Config) withDefaults() Config {
	if c.MCPNamespace == "" {
		c.MCPNamespace = "infra-mcp"
	}
	if c.MCPServersKVPath == "" {
		c.MCPServersKVPath = "infra/mcp-servers"
	}
	if c.LLMProvidersKVPath == "" {
		c.LLMProvidersKVPath = "infra/llm-providers"
	}
	if c.BlueprintsKVPath == "" {
		c.BlueprintsKVPath = "infra/blueprints"
	}
	return c
}

// Service orchestrates the onboarding flows over the store, Nomad, the MCP
// gateway, the LLM gateway, and Vault.
type Service struct {
	store     store.Store
	nomad     NomadClient
	gateway   mcpgw.Client
	llm       llmgw.Client
	vault     VaultClient
	validator blueprintValidator
	cfg       Config
}

// New builds the onboarding service.
func New(st store.Store, nomad NomadClient, gateway mcpgw.Client, llm llmgw.Client, vault VaultClient, validator blueprintValidator, cfg Config) *Service {
	return &Service{store: st, nomad: nomad, gateway: gateway, llm: llm, vault: vault, validator: validator, cfg: cfg.withDefaults()}
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// providerRE constrains a provider name to a single safe Vault path segment: no
// slashes or dots, so it can never traverse outside the llm-providers/ prefix.
var providerRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ---- MCP server onboarding ----

// DeployMCPInput is a request to deploy an existing MCP server as a Nomad job.
type DeployMCPInput struct {
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	SecretRefs map[string]string `json:"secret_refs,omitempty"`
	Transport  string            `json:"transport"`
	Port       int               `json:"port"`
	Path       string            `json:"path,omitempty"`
}

func (in DeployMCPInput) validate() error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("name must be 3-40 chars, lowercase alphanumeric or dashes: %w", apperr.ErrBadRequest)
	}
	if in.Image == "" {
		return fmt.Errorf("image is required: %w", apperr.ErrBadRequest)
	}
	if _, ok := defaultPaths[in.Transport]; !ok {
		return fmt.Errorf("transport must be sse or streamable-http (stdio needs the auth wrapper): %w", apperr.ErrBadRequest)
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("port must be 1-65535: %w", apperr.ErrBadRequest)
	}
	return nil
}

// DeployMCPServer renders and registers the Nomad job, resolves its placement to
// build the gateway peer URL, and records the server as "deployed" (not yet
// published — it must pass the consumption-mirror test first).
func (s *Service) DeployMCPServer(ctx context.Context, actor string, in DeployMCPInput) (store.MCPServer, error) {
	if err := in.validate(); err != nil {
		return store.MCPServer{}, err
	}
	if err := s.ensurePortFree(in.Port, in.Name); err != nil {
		return store.MCPServer{}, err
	}

	prev, _ := s.store.GetMCPServer(ctx, in.Name)
	srv := store.MCPServer{
		Name:       in.Name,
		Image:      in.Image,
		Command:    in.Command,
		Env:        in.Env,
		SecretRefs: in.SecretRefs,
		Transport:  in.Transport,
		Port:       in.Port,
		Path:       in.Path,
		Namespace:  s.cfg.MCPNamespace,
		Status:     store.StatusDeployed,
		Version:    prev.Version + 1,
		CreatedBy:  actor,
	}

	jobID, err := s.nomad.RegisterJob(s.cfg.MCPNamespace, renderMCPJobHCL(srv, s.cfg), "")
	if err != nil {
		s.audit(ctx, actor, "mcp-server.deploy", in.Name, "error")
		return store.MCPServer{}, err
	}
	srv.JobID = jobID

	ip, err := s.nomad.ResolvePlacementIP(s.cfg.MCPNamespace, jobID)
	if err != nil {
		s.audit(ctx, actor, "mcp-server.deploy", in.Name, "error")
		return store.MCPServer{}, err
	}
	srv.GatewayURL = peerURL(ip, srv)

	saved, err := s.store.UpsertMCPServer(ctx, srv)
	if err != nil {
		return store.MCPServer{}, err
	}
	s.audit(ctx, actor, "mcp-server.deploy", in.Name, "ok")
	return saved, nil
}

// TestMCPServer runs the consumption-mirror verification: register the deployed
// server as a gateway peer, discover its tools, compose a (temporary) virtual
// server scoped to them, mint a scoped token, and confirm the token reaches only
// its own server (200) and is denied admin and a decoy server (403). The temporary
// virtual servers and token are torn down; the peer registration is kept for publish.
func (s *Service) TestMCPServer(ctx context.Context, actor, name string) (store.MCPServer, error) {
	srv, err := s.store.GetMCPServer(ctx, name)
	if err != nil {
		return store.MCPServer{}, err
	}

	peerID, err := s.gateway.RegisterPeer(ctx, serviceName(name), srv.GatewayURL)
	if err != nil {
		s.audit(ctx, actor, "mcp-server.test", name, "error")
		return store.MCPServer{}, err
	}
	srv.PeerID = peerID

	toolIDs, err := s.gateway.DiscoverTools(ctx, peerID)
	if err != nil {
		s.audit(ctx, actor, "mcp-server.test", name, "error")
		return store.MCPServer{}, err
	}

	vsID, err := s.gateway.CreateVirtualServer(ctx, serviceName(name)+"-test", "consumption-mirror test for "+name, toolIDs)
	if err != nil {
		return store.MCPServer{}, err
	}
	decoyID, err := s.gateway.CreateVirtualServer(ctx, serviceName(name)+"-decoy", "isolation decoy for "+name, toolIDs)
	if err != nil {
		return store.MCPServer{}, err
	}
	tokenName := serviceName(name) + "-test-" + randHex(4)
	token, err := s.gateway.CreateScopedToken(ctx, tokenName, 1, vsID)
	if err != nil {
		return store.MCPServer{}, err
	}

	probe, err := s.gateway.ProbeScopedToken(ctx, token, vsID, decoyID)
	if err != nil {
		return store.MCPServer{}, err
	}

	// Teardown the temporary verification artifacts; the deployed server and its
	// peer registration stay (publish reuses the peer).
	_ = s.gateway.RevokeTokensByPrefix(ctx, serviceName(name)+"-test-")
	_ = s.gateway.DeleteVirtualServer(ctx, vsID)
	_ = s.gateway.DeleteVirtualServer(ctx, decoyID)

	result := &store.MCPTestResult{
		Passed:             probe.Passed() && len(toolIDs) > 0,
		ToolsDiscovered:    len(toolIDs),
		OwnServerOK:        probe.OwnServerOK,
		AdminDenied:        probe.AdminDenied,
		OtherServerDenied:  probe.OtherServerDenied,
		OtherServerChecked: probe.OtherServerChecked,
		At:                 time.Now(),
	}
	if !result.Passed {
		result.Message = "consumption-mirror checks did not all pass"
	}
	srv.TestResult = result

	saved, err := s.store.UpsertMCPServer(ctx, srv)
	if err != nil {
		return store.MCPServer{}, err
	}
	s.audit(ctx, actor, "mcp-server.test", name, outcome(result.Passed))
	return saved, nil
}

// PublishMCPServer makes a tested server discoverable by Project Admins: it
// requires a green consumption-mirror test, ensures the gateway peer is
// registered, writes the deploy descriptor to Vault KV, and flips to published.
func (s *Service) PublishMCPServer(ctx context.Context, actor, name string, blueprintRef *store.BlueprintRef) (store.MCPServer, error) {
	srv, err := s.store.GetMCPServer(ctx, name)
	if err != nil {
		return store.MCPServer{}, err
	}
	// Validate any supplied blueprint_ref before the already-published short-circuit
	// so a re-publish with a bad ref is still rejected rather than silently accepted.
	if blueprintRef != nil {
		bp, err := s.store.GetBlueprint(ctx, blueprintRef.ID, blueprintRef.Version)
		if err != nil {
			return store.MCPServer{}, fmt.Errorf("blueprint %s@%d: %w", blueprintRef.ID, blueprintRef.Version, apperr.ErrBadRequest)
		}
		if bp.Status != store.StatusPublished {
			return store.MCPServer{}, fmt.Errorf("blueprint %s@%d is not published: %w", blueprintRef.ID, blueprintRef.Version, apperr.ErrBadRequest)
		}
		if bp.ContentHash != blueprintRef.ContentHash {
			return store.MCPServer{}, fmt.Errorf("blueprint_ref content hash does not match published blueprint: %w", apperr.ErrBadRequest)
		}
		srv.BlueprintRef = blueprintRef
	}
	if srv.Status == store.StatusPublished {
		return srv, nil
	}
	if srv.TestResult == nil || !srv.TestResult.Passed {
		return store.MCPServer{}, fmt.Errorf("server %q must pass the consumption-mirror test before publish: %w", name, apperr.ErrConflict)
	}

	peerID, err := s.gateway.RegisterPeer(ctx, serviceName(name), srv.GatewayURL)
	if err != nil {
		s.audit(ctx, actor, "mcp-server.publish", name, "error")
		return store.MCPServer{}, err
	}
	srv.PeerID = peerID

	descriptor := map[string]any{
		"image":        srv.Image,
		"transport":    srv.Transport,
		"gateway_url":  srv.GatewayURL,
		"peer_id":      srv.PeerID,
		"version":      srv.Version,
		"published_by": actor,
		"published_at": time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.vault.WriteKV(ctx, s.cfg.MCPServersKVPath+"/"+name, descriptor); err != nil {
		s.audit(ctx, actor, "mcp-server.publish", name, "error")
		return store.MCPServer{}, err
	}

	srv.Status = store.StatusPublished
	saved, err := s.store.UpsertMCPServer(ctx, srv)
	if err != nil {
		return store.MCPServer{}, err
	}
	s.audit(ctx, actor, "mcp-server.publish", name, "ok")
	return saved, nil
}

// ListMCPServers returns every server the platform has deployed.
func (s *Service) ListMCPServers(ctx context.Context) ([]store.MCPServer, error) {
	return s.store.ListMCPServers(ctx)
}

// ListPublishedServerTypes returns the published MCP servers a Project Admin may
// deploy into their project. Only published servers are returned.
func (s *Service) ListPublishedServerTypes(ctx context.Context) ([]store.MCPServer, error) {
	all, err := s.store.ListMCPServers(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.MCPServer{}
	for _, srv := range all {
		if srv.Status == store.StatusPublished {
			out = append(out, srv)
		}
	}
	return out, nil
}

// DeleteMCPServer purges the Nomad job, removes the gateway peer, and drops the
// store row. Best-effort on the external systems so a partial state can be cleaned.
func (s *Service) DeleteMCPServer(ctx context.Context, actor, name string) error {
	srv, err := s.store.GetMCPServer(ctx, name)
	if err != nil {
		return err
	}
	if srv.JobID != "" {
		_ = s.nomad.PurgeJob(s.cfg.MCPNamespace, srv.JobID)
	}
	if srv.PeerID != "" {
		_ = s.gateway.DeletePeer(ctx, srv.PeerID)
	}
	if err := s.store.DeleteMCPServer(ctx, name); err != nil {
		return err
	}
	s.audit(ctx, actor, "mcp-server.delete", name, "ok")
	return nil
}

// ---- LLM model onboarding ----

// SetProviderKeyInput writes a provider's API key into Vault. The key is
// write-only: OnboardLLMModel reads it back at call time, but it is never
// returned, logged, or stored in the control-plane DB.
type SetProviderKeyInput struct {
	Provider string `json:"provider"`
	APIKey   string `json:"api_key"`
}

func (in SetProviderKeyInput) validate() error {
	if !providerRE.MatchString(in.Provider) {
		return fmt.Errorf("provider must be 1-40 chars, lowercase alphanumeric or dashes: %w", apperr.ErrBadRequest)
	}
	if in.APIKey == "" {
		return fmt.Errorf("api_key is required: %w", apperr.ErrBadRequest)
	}
	return nil
}

// SetProviderKey stores a provider's API key at LLMProvidersKVPath/<provider> so
// OnboardLLMModel can inject it at call time. Write-only: only the provider name
// is audited; the key itself is never logged or echoed back.
func (s *Service) SetProviderKey(ctx context.Context, actor string, in SetProviderKeyInput) error {
	if err := in.validate(); err != nil {
		return err
	}
	path := s.cfg.LLMProvidersKVPath + "/" + in.Provider
	if err := s.vault.WriteKV(ctx, path, map[string]any{"api_key": in.APIKey}); err != nil {
		s.audit(ctx, actor, "llm-provider.set-key", in.Provider, "error")
		return err
	}
	s.audit(ctx, actor, "llm-provider.set-key", in.Provider, "ok")
	return nil
}

// OnboardLLMInput is a request to onboard a model into the LiteLLM gateway.
type OnboardLLMInput struct {
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	BackendModel string `json:"backend_model"`
}

func (in OnboardLLMInput) validate() error {
	if in.Name == "" || in.Provider == "" || in.BackendModel == "" {
		return fmt.Errorf("name, provider and backend_model are required: %w", apperr.ErrBadRequest)
	}
	return nil
}

// OnboardLLMModel reads the provider key from Vault (never stored), registers the
// model in LiteLLM, and records it as "draft". The provider key never touches the
// store — only the provider name.
func (s *Service) OnboardLLMModel(ctx context.Context, actor string, in OnboardLLMInput) (store.LLMModel, error) {
	if err := in.validate(); err != nil {
		return store.LLMModel{}, err
	}
	apiKey, err := s.vault.ReadKVField(ctx, s.cfg.LLMProvidersKVPath+"/"+in.Provider, "api_key")
	if err != nil {
		s.audit(ctx, actor, "llm-model.onboard", in.Name, "error")
		return store.LLMModel{}, err
	}
	if err := s.llm.AddModel(ctx, llmgw.AddModelInput{
		ModelName:     in.Name,
		LiteLLMParams: map[string]any{"model": in.BackendModel, "api_key": apiKey},
	}); err != nil {
		s.audit(ctx, actor, "llm-model.onboard", in.Name, "error")
		return store.LLMModel{}, err
	}

	model := store.LLMModel{
		Name:         in.Name,
		Provider:     in.Provider,
		BackendModel: in.BackendModel,
		Status:       store.StatusDraft,
		CreatedBy:    actor,
		LiteLLMID:    s.resolveLiteLLMID(ctx, in.Name),
	}
	saved, err := s.store.UpsertLLMModel(ctx, model)
	if err != nil {
		return store.LLMModel{}, err
	}
	s.audit(ctx, actor, "llm-model.onboard", in.Name, "ok")
	return saved, nil
}

// TestLLMModel runs the scoped-key consumption-mirror verification: mint a
// budgeted, rpm-limited key, confirm a completion succeeds (200), observe the rpm
// limit (429), revoke the key, and confirm revocation is enforced (401).
func (s *Service) TestLLMModel(ctx context.Context, actor, name string) (store.LLMModel, error) {
	model, err := s.store.GetLLMModel(ctx, name)
	if err != nil {
		return store.LLMModel{}, err
	}

	alias := "llm-test-" + name
	key, err := s.llm.GenerateKey(ctx, llmgw.KeySpec{
		Alias:     alias,
		Models:    []string{name},
		MaxBudget: 1,
		RPMLimit:  1,
		Metadata:  map[string]string{"purpose": "onboarding-test"},
	})
	if err != nil {
		s.audit(ctx, actor, "llm-model.test", name, "error")
		return store.LLMModel{}, err
	}
	// Ensure the test key is revoked even if a step below fails.
	defer func() { _ = s.llm.DeleteKeyByAlias(ctx, alias) }()

	result := &store.LLMTestResult{At: time.Now()}

	st, err := s.llm.TestCompletion(ctx, key, name, "ping")
	if err != nil {
		s.audit(ctx, actor, "llm-model.test", name, "error")
		return store.LLMModel{}, err
	}
	result.CompletionOK = st == 200

	// Best-effort rpm observation: a second immediate call should be rate-limited.
	if st2, err := s.llm.TestCompletion(ctx, key, name, "ping"); err == nil {
		result.RateLimitEnforced = st2 == 429
	}

	// Revoke and confirm the key no longer authenticates.
	if err := s.llm.DeleteKeyByAlias(ctx, alias); err == nil {
		if st3, err := s.llm.TestCompletion(ctx, key, name, "ping"); err == nil {
			result.RevokeEnforced = st3 == 401 || st3 == 403
		}
	}

	result.Passed = result.CompletionOK && result.RevokeEnforced
	if !result.Passed {
		result.Message = "scoped-key completion or revocation check did not pass"
	}
	model.TestResult = result

	saved, err := s.store.UpsertLLMModel(ctx, model)
	if err != nil {
		return store.LLMModel{}, err
	}
	s.audit(ctx, actor, "llm-model.test", name, outcome(result.Passed))
	return saved, nil
}

// PublishLLMModel makes a tested model selectable by Project Admins' keys.
func (s *Service) PublishLLMModel(ctx context.Context, actor, name string) (store.LLMModel, error) {
	model, err := s.store.GetLLMModel(ctx, name)
	if err != nil {
		return store.LLMModel{}, err
	}
	if model.Status == store.StatusPublished {
		return model, nil
	}
	if model.TestResult == nil || !model.TestResult.Passed {
		return store.LLMModel{}, fmt.Errorf("model %q must pass the consumption-mirror test before publish: %w", name, apperr.ErrConflict)
	}
	model.Status = store.StatusPublished
	saved, err := s.store.UpsertLLMModel(ctx, model)
	if err != nil {
		return store.LLMModel{}, err
	}
	s.audit(ctx, actor, "llm-model.publish", name, "ok")
	return saved, nil
}

// LLMModelView is the inventory row the UI renders. The LiteLLM gateway is the
// source of truth for which models exist (Name/Source/LiteLLMID); the Portal
// store contributes only the onboarding overlay (Managed/Status/TestResult/...).
type LLMModelView struct {
	Name         string               `json:"name"`
	Source       string               `json:"source,omitempty"` // "config" | "db" ("" when orphaned)
	LiteLLMID    string               `json:"litellm_id,omitempty"`
	Managed      bool                 `json:"managed"`            // has a Portal overlay row
	Orphaned     bool                 `json:"orphaned,omitempty"` // overlay row with no live gateway model
	Provider     string               `json:"provider,omitempty"`
	BackendModel string               `json:"backend_model,omitempty"`
	Status       string               `json:"status,omitempty"`
	TestResult   *store.LLMTestResult `json:"test_result,omitempty"`
	CreatedBy    string               `json:"created_by,omitempty"`
}

// ListLLMModels reads the live model inventory from the LiteLLM gateway (the
// source of truth) and left-joins the Portal onboarding overlay by name. Models
// the gateway serves without an overlay are "unmanaged" (config-list or added
// out-of-band); overlay rows with no live model are flagged "orphaned" so the
// admin can clean them up. This keeps the UI consistent with what the gateway
// actually serves instead of trusting a separate copy that can drift.
func (s *Service) ListLLMModels(ctx context.Context) ([]LLMModelView, error) {
	live, err := s.llm.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	overlay, err := s.store.ListLLMModels(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.LLMModel, len(overlay))
	for _, m := range overlay {
		byName[m.Name] = m
	}

	views := make([]LLMModelView, 0, len(live)+len(overlay))
	seen := make(map[string]bool, len(live))
	for _, lm := range live {
		seen[lm.Name] = true
		// Seed provider/backend from the gateway so config models (no overlay) still
		// show what they map to; a managed overlay overrides below.
		v := LLMModelView{Name: lm.Name, Source: lm.Source, LiteLLMID: lm.ID, Provider: lm.Provider, BackendModel: lm.Backend}
		if o, ok := byName[lm.Name]; ok {
			v.Managed = true
			v.Provider = o.Provider
			v.BackendModel = o.BackendModel
			v.Status = o.Status
			v.TestResult = o.TestResult
			v.CreatedBy = o.CreatedBy
		}
		views = append(views, v)
	}
	for _, o := range overlay {
		if seen[o.Name] {
			continue
		}
		views = append(views, LLMModelView{
			Name:         o.Name,
			LiteLLMID:    o.LiteLLMID,
			Managed:      true,
			Orphaned:     true,
			Provider:     o.Provider,
			BackendModel: o.BackendModel,
			Status:       o.Status,
			TestResult:   o.TestResult,
			CreatedBy:    o.CreatedBy,
		})
	}
	return views, nil
}

// DeleteLLMModel removes the model from LiteLLM and drops the store row.
func (s *Service) DeleteLLMModel(ctx context.Context, actor, name string) error {
	model, err := s.store.GetLLMModel(ctx, name)
	if err != nil {
		return err
	}
	if model.LiteLLMID != "" {
		_ = s.llm.DeleteModel(ctx, model.LiteLLMID)
	}
	if err := s.store.DeleteLLMModel(ctx, name); err != nil {
		return err
	}
	s.audit(ctx, actor, "llm-model.delete", name, "ok")
	return nil
}

// ---- credential blueprint authoring ----

// CreateBlueprintDraft validates a manifest's shape, stores its control-plane row
// (status draft), and writes the canonical manifest JSON to Vault KV (immutable per
// id/version). Secret material is never in the manifest — params are declarations.
func (s *Service) CreateBlueprintDraft(ctx context.Context, actor string, m blueprint.BlueprintManifest) (store.Blueprint, error) {
	if err := m.Validate(); err != nil {
		return store.Blueprint{}, err
	}
	manifestJSON, err := json.Marshal(m)
	if err != nil {
		return store.Blueprint{}, fmt.Errorf("admin: marshal manifest: %w", err)
	}
	kvPath := fmt.Sprintf("%s/%s/%d", s.cfg.BlueprintsKVPath, m.ID, m.Version)
	if err := s.vault.WriteKV(ctx, kvPath, map[string]any{"manifest": string(manifestJSON)}); err != nil {
		s.audit(ctx, actor, "blueprint.create", m.ID, "error")
		return store.Blueprint{}, err
	}
	bp := store.Blueprint{
		ID: m.ID, Version: m.Version, Class: m.Class, ContentHash: m.ContentHash(),
		Status: store.StatusDraft, CreatedBy: actor,
	}
	saved, err := s.store.UpsertBlueprint(ctx, bp)
	if err != nil {
		return store.Blueprint{}, err
	}
	s.audit(ctx, actor, "blueprint.create", m.ID, "ok")
	return saved, nil
}

// ValidateBlueprint runs the publish gate (lint + live consumption-mirror) and
// records the result; on success the blueprint advances to validated.
func (s *Service) ValidateBlueprint(ctx context.Context, actor, id string, version int) (store.Blueprint, error) {
	bp, err := s.store.GetBlueprint(ctx, id, version)
	if err != nil {
		return store.Blueprint{}, err
	}
	m, err := s.readManifest(ctx, id, version)
	if err != nil {
		return store.Blueprint{}, err
	}
	result, err := s.validator.Validate(ctx, m)
	if err != nil {
		s.audit(ctx, actor, "blueprint.validate", id, "error")
		return store.Blueprint{}, err
	}
	raw, _ := json.Marshal(result)
	bp.Validation = raw
	if result.Passed {
		bp.Status = store.StatusValidated
	}
	saved, err := s.store.UpsertBlueprint(ctx, bp)
	if err != nil {
		return store.Blueprint{}, err
	}
	s.audit(ctx, actor, "blueprint.validate", id, outcome(result.Passed))
	return saved, nil
}

// PublishBlueprint makes a validated blueprint catalog-bindable. Requires a green
// validation (status validated).
func (s *Service) PublishBlueprint(ctx context.Context, actor, id string, version int) (store.Blueprint, error) {
	bp, err := s.store.GetBlueprint(ctx, id, version)
	if err != nil {
		return store.Blueprint{}, err
	}
	if bp.Status == store.StatusPublished {
		return bp, nil
	}
	if bp.Status != store.StatusValidated {
		return store.Blueprint{}, fmt.Errorf("blueprint %s@%d must pass validation before publish: %w", id, version, apperr.ErrConflict)
	}
	bp.Status = store.StatusPublished
	saved, err := s.store.UpsertBlueprint(ctx, bp)
	if err != nil {
		return store.Blueprint{}, err
	}
	s.audit(ctx, actor, "blueprint.publish", id, "ok")
	return saved, nil
}

// ListBlueprints returns every authored blueprint.
func (s *Service) ListBlueprints(ctx context.Context) ([]store.Blueprint, error) {
	return s.store.ListBlueprints(ctx)
}

// readManifest reads the canonical manifest JSON back from Vault KV.
func (s *Service) readManifest(ctx context.Context, id string, version int) (blueprint.BlueprintManifest, error) {
	kvPath := fmt.Sprintf("%s/%s/%d", s.cfg.BlueprintsKVPath, id, version)
	raw, err := s.vault.ReadKVField(ctx, kvPath, "manifest")
	if err != nil {
		return blueprint.BlueprintManifest{}, err
	}
	var m blueprint.BlueprintManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return blueprint.BlueprintManifest{}, fmt.Errorf("admin: parse stored manifest: %w", err)
	}
	return m, nil
}

// ListAudit returns the most recent admin audit events.
func (s *Service) ListAudit(ctx context.Context, limit int) ([]store.AuditEvent, error) {
	return s.store.ListAudit(ctx, limit)
}

// ---- helpers ----

// ensurePortFree rejects a static host port already reserved by another job. A
// re-deploy of the same server keeps its own port.
func (s *Service) ensurePortFree(port int, name string) error {
	used, err := s.nomad.UsedPorts()
	if err != nil {
		return err
	}
	existing, _ := s.store.GetMCPServer(context.Background(), name)
	for _, p := range used {
		if p == port && p != existing.Port {
			return fmt.Errorf("host port %d is already in use: %w", port, apperr.ErrConflict)
		}
	}
	return nil
}

// resolveLiteLLMID looks up the LiteLLM id assigned to a model name (best-effort;
// an empty id just means delete will fall back to a no-op).
func (s *Service) resolveLiteLLMID(ctx context.Context, name string) string {
	models, err := s.llm.ListModels(ctx)
	if err != nil {
		return ""
	}
	for _, m := range models {
		if m.Name == name {
			return m.ID
		}
	}
	return ""
}

func (s *Service) audit(ctx context.Context, actor, action, target, outcome string) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome})
}

func outcome(passed bool) string {
	if passed {
		return "passed"
	}
	return "failed"
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "xxxxxxxx"[:2*n]
	}
	return hex.EncodeToString(b)
}
