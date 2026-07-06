// Package admin is the Platform Admin onboarding plane: it onboards LLM models
// into the LiteLLM gateway and verifies each the way a project will consume it
// (a scoped budgeted/rate-limited key). MCP servers are project-owned — a
// project-admin deploys them directly (internal/projectadmin), so no MCP or
// blueprint plane exists here anymore.
//
// All onboarding logic lives in Service (API-first): the HTTP handlers are thin
// adapters over these methods, so the same operations can back a programmatic
// onboarding API later. Every mutation writes an audit event.
package admin

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// VaultClient is the KV access the admin plane needs (satisfied by *hashistack.Vault).
type VaultClient interface {
	// ReadKVField reads a single field from a KV-v2 secret at relPath.
	ReadKVField(ctx context.Context, relPath, field string) (string, error)
	// WriteKV writes data to a KV-v2 secret at relPath.
	WriteKV(ctx context.Context, relPath string, data map[string]any) error
}

// Config tunes the onboarding plane.
type Config struct {
	LLMProvidersKVPath string // KV path prefix for provider keys (e.g. "infra/llm-providers")
}

func (c Config) withDefaults() Config {
	if c.LLMProvidersKVPath == "" {
		c.LLMProvidersKVPath = "infra/llm-providers"
	}
	return c
}

// Service orchestrates the onboarding flows over the store, the LLM gateway,
// and Vault.
type Service struct {
	store store.Store
	llm   llmgw.Client
	vault VaultClient
	cfg   Config
}

// New builds the onboarding service.
func New(st store.Store, llm llmgw.Client, vault VaultClient, cfg Config) *Service {
	return &Service{store: st, llm: llm, vault: vault, cfg: cfg.withDefaults()}
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)

// providerRE constrains a provider name to a single safe Vault path segment: no
// slashes or dots, so it can never traverse outside the llm-providers/ prefix.
var providerRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

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

func (s *Service) ListAudit(ctx context.Context, limit int) ([]store.AuditEvent, error) {
	return s.store.ListAudit(ctx, limit)
}

// ---- helpers ----

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
