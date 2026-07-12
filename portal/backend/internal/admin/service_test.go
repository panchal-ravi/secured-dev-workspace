package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// ---- fakes ----

type fakeVault struct {
	provider map[string]string
	written  map[string]map[string]any
}

func (f *fakeVault) ReadKVField(_ context.Context, path, field string) (string, error) {
	if v, ok := f.provider[path+"#"+field]; ok {
		return v, nil
	}
	if data, ok := f.written[path]; ok {
		if v, ok := data[field].(string); ok {
			return v, nil
		}
	}
	return "", errors.New("vault: not found")
}
func (f *fakeVault) WriteKV(_ context.Context, path string, data map[string]any) error {
	if f.written == nil {
		f.written = map[string]map[string]any{}
	}
	f.written[path] = data
	return nil
}

type fakeLLM struct {
	added       []llmgw.AddModelInput
	completions int
}

func (f *fakeLLM) AddModel(_ context.Context, in llmgw.AddModelInput) error {
	f.added = append(f.added, in)
	return nil
}
func (f *fakeLLM) ListModels(_ context.Context) ([]llmgw.Model, error) {
	out := make([]llmgw.Model, 0, len(f.added))
	for _, a := range f.added {
		backend, _ := a.LiteLLMParams["model"].(string)
		provider := ""
		if i := strings.Index(backend, "/"); i > 0 {
			provider = backend[:i]
		}
		out = append(out, llmgw.Model{Name: a.ModelName, ID: "id-" + a.ModelName, Source: "db", Backend: backend, Provider: provider})
	}
	return out, nil
}
func (f *fakeLLM) DeleteModel(_ context.Context, id string) error { return nil }
func (f *fakeLLM) GenerateKey(_ context.Context, spec llmgw.KeySpec) (string, error) {
	return "sk-test", nil
}
func (f *fakeLLM) DeleteKeyByAlias(_ context.Context, alias string) error { return nil }
func (f *fakeLLM) TestCompletion(_ context.Context, key, model, prompt string) (int, error) {
	f.completions++
	switch f.completions {
	case 1:
		return 200, nil // success
	case 2:
		return 429, nil // rate limited
	default:
		return 401, nil // after revoke
	}
}

func newService(l *fakeLLM, v *fakeVault) (*Service, store.Store) {
	st := store.NewMemory()
	return New(st, l, v, Config{}), st
}

func TestLLMOnboardTestPublish(t *testing.T) {
	l := &fakeLLM{}
	v := &fakeVault{provider: map[string]string{"infra/llm-providers/deepseek#api_key": "sk-deepseek"}}
	svc, _ := newService(l, v)
	ctx := context.Background()

	model, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "deepseek-v4-flash", Provider: "deepseek", BackendModel: "deepseek/deepseek-chat"})
	if err != nil {
		t.Fatalf("OnboardLLMModel: %v", err)
	}
	if model.Status != store.StatusDraft || model.LiteLLMID != "id-deepseek-v4-flash" {
		t.Fatalf("unexpected model: %+v", model)
	}
	if len(l.added) != 1 || l.added[0].LiteLLMParams["api_key"] != "sk-deepseek" {
		t.Fatalf("provider key not injected: %+v", l.added)
	}

	// publish before test rejected
	if _, err := svc.PublishLLMModel(ctx, "admin@x", "deepseek-v4-flash"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("publish before test: want ErrConflict, got %v", err)
	}

	tested, err := svc.TestLLMModel(ctx, "admin@x", "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("TestLLMModel: %v", err)
	}
	tr := tested.TestResult
	if tr == nil || !tr.Passed || !tr.CompletionOK || !tr.RateLimitEnforced || !tr.RevokeEnforced {
		t.Fatalf("unexpected llm test result: %+v", tr)
	}

	pub, err := svc.PublishLLMModel(ctx, "admin@x", "deepseek-v4-flash")
	if err != nil || pub.Status != store.StatusPublished {
		t.Fatalf("PublishLLMModel: %+v err=%v", pub, err)
	}
}

func TestSetProviderKeyWritesToVault(t *testing.T) {
	v := &fakeVault{provider: map[string]string{}}
	svc, _ := newService(&fakeLLM{}, v)

	if err := svc.SetProviderKey(context.Background(), "admin@x", SetProviderKeyInput{Provider: "deepseek", APIKey: "sk-secret"}); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	got := v.written["infra/llm-providers/deepseek"]
	if got == nil || got["api_key"] != "sk-secret" {
		t.Fatalf("want api_key written to infra/llm-providers/deepseek, got %v", got)
	}
}

func TestSetProviderKeyValidation(t *testing.T) {
	cases := []SetProviderKeyInput{
		{Provider: "deepseek", APIKey: ""},               // empty key
		{Provider: "", APIKey: "sk"},                     // empty provider
		{Provider: "../infra/llm-gateway", APIKey: "sk"}, // path traversal
		{Provider: "Deepseek", APIKey: "sk"},             // uppercase
	}
	for i, in := range cases {
		v := &fakeVault{provider: map[string]string{}}
		svc, _ := newService(&fakeLLM{}, v)
		if err := svc.SetProviderKey(context.Background(), "admin@x", in); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("case %d: want ErrBadRequest, got %v", i, err)
		}
		if len(v.written) != 0 {
			t.Fatalf("case %d: nothing should be written on validation failure, got %v", i, v.written)
		}
	}
}

// TestSetProviderKeyThenOnboard proves the round-trip the UI relies on: a key set
// via SetProviderKey is the same one OnboardLLMModel reads back from Vault.
func TestSetProviderKeyThenOnboard(t *testing.T) {
	l := &fakeLLM{}
	v := &fakeVault{provider: map[string]string{}}
	svc, _ := newService(l, v)
	ctx := context.Background()

	if err := svc.SetProviderKey(ctx, "admin@x", SetProviderKeyInput{Provider: "deepseek", APIKey: "sk-roundtrip"}); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if _, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "test-llm-model", Provider: "deepseek", BackendModel: "deepseek/deepseek-chat"}); err != nil {
		t.Fatalf("OnboardLLMModel after SetProviderKey: %v", err)
	}
	if len(l.added) != 1 || l.added[0].LiteLLMParams["api_key"] != "sk-roundtrip" {
		t.Fatalf("onboarded model did not pick up the set provider key: %+v", l.added)
	}
}

// TestListLLMModelsJoinsGatewayWithOverlay proves the inventory is driven by the
// LiteLLM gateway and annotated with the Portal overlay: a managed model, an
// unmanaged gateway model, and an orphaned overlay row each render distinctly.
func TestListLLMModelsJoinsGatewayWithOverlay(t *testing.T) {
	l := &fakeLLM{}
	v := &fakeVault{provider: map[string]string{"infra/llm-providers/deepseek#api_key": "sk-deepseek"}}
	svc, st := newService(l, v)
	ctx := context.Background()

	// managed: onboarded through the Portal (live in gateway + overlay row).
	if _, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "managed-model", Provider: "deepseek", BackendModel: "deepseek/deepseek-chat"}); err != nil {
		t.Fatalf("OnboardLLMModel: %v", err)
	}
	// unmanaged: present in the gateway but with no overlay row.
	l.added = append(l.added, llmgw.AddModelInput{ModelName: "config-model", LiteLLMParams: map[string]any{"model": "deepseek/deepseek-chat"}})
	// orphaned: overlay row with no live gateway model.
	if _, err := st.UpsertLLMModel(ctx, store.LLMModel{Name: "ghost", Status: store.StatusDraft}); err != nil {
		t.Fatalf("seed orphan: %v", err)
	}

	views, err := svc.ListLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListLLMModels: %v", err)
	}
	got := map[string]LLMModelView{}
	for _, v := range views {
		got[v.Name] = v
	}
	if m := got["managed-model"]; !m.Managed || m.Orphaned || m.Status != store.StatusDraft || m.LiteLLMID == "" {
		t.Fatalf("managed-model: %+v", m)
	}
	// config-model is unmanaged but its provider/backend still come from the gateway.
	if c := got["config-model"]; c.Managed || c.Orphaned || c.Provider != "deepseek" || c.BackendModel != "deepseek/deepseek-chat" {
		t.Fatalf("config-model should be unmanaged and live with gateway-sourced provider/backend: %+v", c)
	}
	if g, ok := got["ghost"]; !ok || !g.Orphaned || !g.Managed {
		t.Fatalf("ghost should be an orphaned overlay row: %+v", g)
	}
}

func TestLLMOnboardMissingProviderKey(t *testing.T) {
	svc, _ := newService(&fakeLLM{}, &fakeVault{provider: map[string]string{}})
	ctx := context.Background()
	// provider key not in Vault → upstream error (not a client-class apperr)
	if _, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "m", Provider: "nope", BackendModel: "x/y"}); err == nil {
		t.Fatalf("expected error when provider key is absent")
	}
}
