package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// ---- fakes ----

type fakeNomad struct {
	usedPorts []int
	lastHCL   string
	purged    bool
}

func (f *fakeNomad) RegisterJob(ns, hcl, flavor string) (string, error) {
	f.lastHCL = hcl
	return "jobid", nil
}
func (f *fakeNomad) ResolvePlacementIP(ns, jobID string) (string, error) { return "10.0.0.5", nil }
func (f *fakeNomad) PurgeJob(ns, jobID string) error                     { f.purged = true; return nil }
func (f *fakeNomad) UsedPorts() ([]int, error)                           { return f.usedPorts, nil }

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

type fakeGateway struct {
	probe         mcpgw.ScopeProbe
	tools         []string
	deletedPeer   bool
	lastTransport string
}

func (f *fakeGateway) RegisterPeer(_ context.Context, name, url, transport string) (string, error) {
	f.lastTransport = transport
	return "peer-" + name, nil
}
func (f *fakeGateway) DiscoverTools(_ context.Context, peerID string) ([]string, error) {
	return f.tools, nil
}
func (f *fakeGateway) CreateVirtualServer(_ context.Context, name, desc string, toolIDs []string) (string, error) {
	return "vs-" + name, nil
}
func (f *fakeGateway) CreateScopedToken(_ context.Context, name string, days int, serverID string) (string, error) {
	return "scoped-token", nil
}
func (f *fakeGateway) RevokeTokensByPrefix(_ context.Context, prefix string) error { return nil }
func (f *fakeGateway) ProbeScopedToken(_ context.Context, token, serverID, otherServerID string) (mcpgw.ScopeProbe, error) {
	return f.probe, nil
}
func (f *fakeGateway) DeleteVirtualServer(_ context.Context, serverID string) error { return nil }
func (f *fakeGateway) DeletePeer(_ context.Context, peerID string) error {
	f.deletedPeer = true
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

func passingProbe() mcpgw.ScopeProbe {
	return mcpgw.ScopeProbe{OwnServerOK: true, AdminDenied: true, OtherServerChecked: true, OtherServerDenied: true}
}

func newService(n *fakeNomad, g *fakeGateway, l *fakeLLM, v *fakeVault) (*Service, store.Store) {
	st := store.NewMemory()
	return New(st, n, g, l, v, nil, Config{MCPJobVaultRole: "infra-mcp-job"}), st
}

// ---- MCP flow ----

func TestMCPDeployTestPublish(t *testing.T) {
	n := &fakeNomad{}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1", "t2"}}
	v := &fakeVault{provider: map[string]string{}}
	svc, _ := newService(n, g, &fakeLLM{}, v)
	ctx := context.Background()

	in := DeployMCPInput{Name: "vault-mcp", Image: "hashicorp/vault-mcp-server", Transport: "sse", Port: 8080}
	srv, err := svc.DeployMCPServer(ctx, "admin@x", in)
	if err != nil {
		t.Fatalf("DeployMCPServer: %v", err)
	}
	if srv.Status != store.StatusDeployed || srv.JobID != "jobid" {
		t.Fatalf("unexpected deploy state: %+v", srv)
	}
	if srv.GatewayURL != "http://10.0.0.5:8080/sse" {
		t.Fatalf("GatewayURL = %q", srv.GatewayURL)
	}
	if !strings.Contains(n.lastHCL, "image      = \"hashicorp/vault-mcp-server\"") {
		t.Fatalf("rendered HCL missing image:\n%s", n.lastHCL)
	}

	// publish before a green test is rejected
	if _, err := svc.PublishMCPServer(ctx, "admin@x", "vault-mcp", nil); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("publish before test: want ErrConflict, got %v", err)
	}

	tested, err := svc.TestMCPServer(ctx, "admin@x", "vault-mcp")
	if err != nil {
		t.Fatalf("TestMCPServer: %v", err)
	}
	if tested.TestResult == nil || !tested.TestResult.Passed || tested.TestResult.ToolsDiscovered != 2 {
		t.Fatalf("unexpected test result: %+v", tested.TestResult)
	}
	if g.lastTransport != "sse" {
		t.Fatalf("RegisterPeer transport = %q, want sse", g.lastTransport)
	}

	pub, err := svc.PublishMCPServer(ctx, "admin@x", "vault-mcp", nil)
	if err != nil {
		t.Fatalf("PublishMCPServer: %v", err)
	}
	if pub.Status != store.StatusPublished {
		t.Fatalf("status = %q, want published", pub.Status)
	}
	if _, ok := v.written["infra/mcp-servers/vault-mcp"]; !ok {
		t.Fatalf("publish did not write Vault descriptor: %v", v.written)
	}
}

func TestDeployInjectVaultToken(t *testing.T) {
	ctx := context.Background()

	// Guard: flag set but no MCPJobVaultRole configured (infra not applied) → 400,
	// and nothing is registered with Nomad.
	n := &fakeNomad{}
	noRole := New(store.NewMemory(), n, &fakeGateway{}, &fakeLLM{}, &fakeVault{}, nil, Config{})
	_, err := noRole.DeployMCPServer(ctx, "admin@x", DeployMCPInput{
		Name: "vault-mcp", Image: "img", Transport: "streamable-http", Port: 8080, InjectVaultToken: true,
	})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("guard: want ErrBadRequest, got %v", err)
	}
	if n.lastHCL != "" {
		t.Fatalf("guard should not register a job, got HCL:\n%s", n.lastHCL)
	}

	// Happy path: role configured → deploy renders the bare vault{role} block.
	n2 := &fakeNomad{}
	svc, _ := newService(n2, &fakeGateway{probe: passingProbe(), tools: []string{"t1"}}, &fakeLLM{}, &fakeVault{})
	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{
		Name: "vault-mcp", Image: "img", Transport: "streamable-http", Port: 8080, InjectVaultToken: true,
	}); err != nil {
		t.Fatalf("deploy with role: %v", err)
	}
	if !strings.Contains(n2.lastHCL, `role = "infra-mcp-job"`) {
		t.Fatalf("rendered HCL missing vault role:\n%s", n2.lastHCL)
	}
}

func TestPublishBindsBlueprintRef(t *testing.T) {
	n := &fakeNomad{}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1"}}
	v := &fakeVault{provider: map[string]string{}}
	svc, st := newService(n, g, &fakeLLM{}, v)
	ctx := context.Background()

	if _, err := st.UpsertBlueprint(ctx, store.Blueprint{ID: "postgres-mcp", Version: 1, Class: "A", ContentHash: "h1", Status: store.StatusPublished}); err != nil {
		t.Fatalf("seed blueprint: %v", err)
	}
	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{Name: "postgres-mcp", Image: "img", Transport: "sse", Port: 8080}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := svc.TestMCPServer(ctx, "admin@x", "postgres-mcp"); err != nil {
		t.Fatalf("test: %v", err)
	}
	ref := &store.BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: "h1"}
	pub, err := svc.PublishMCPServer(ctx, "admin@x", "postgres-mcp", ref)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.BlueprintRef == nil || pub.BlueprintRef.ContentHash != "h1" {
		t.Fatalf("blueprint_ref not bound: %+v", pub.BlueprintRef)
	}

	types, err := svc.ListPublishedServerTypes(ctx)
	if err != nil || len(types) != 1 || types[0].BlueprintRef == nil {
		t.Fatalf("published types: %+v err=%v", types, err)
	}

	if _, err := svc.PublishMCPServer(ctx, "admin@x", "postgres-mcp", &store.BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: "WRONG"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("mismatched ref: want ErrBadRequest, got %v", err)
	}
}

func TestMCPFailingProbeBlocksPublish(t *testing.T) {
	n := &fakeNomad{}
	g := &fakeGateway{probe: mcpgw.ScopeProbe{OwnServerOK: true, AdminDenied: false}, tools: []string{"t1"}}
	svc, _ := newService(n, g, &fakeLLM{}, &fakeVault{})
	ctx := context.Background()

	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{Name: "bad-mcp", Image: "img", Transport: "streamable-http", Port: 8080}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	tested, _ := svc.TestMCPServer(ctx, "admin@x", "bad-mcp")
	if tested.TestResult.Passed {
		t.Fatalf("test should fail when admin access is not denied")
	}
	if _, err := svc.PublishMCPServer(ctx, "admin@x", "bad-mcp", nil); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("publish after failed test: want ErrConflict, got %v", err)
	}
}

func TestMCPValidation(t *testing.T) {
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, &fakeLLM{}, &fakeVault{})
	ctx := context.Background()
	cases := []DeployMCPInput{
		{Name: "x", Image: "img", Transport: "sse", Port: 80},         // name too short
		{Name: "ok-name", Image: "", Transport: "sse", Port: 80},      // no image
		{Name: "ok-name", Image: "img", Transport: "stdio", Port: 80}, // stdio not allowed
		{Name: "ok-name", Image: "img", Transport: "sse", Port: 0},    // bad port
		{Name: "ok-name", Image: "img", Transport: "sse", Port: 9000}, // port above the 8080-8099 band
		{Name: "ok-name", Image: "img", Transport: "sse", Port: 8079}, // port below the band
	}
	for i, in := range cases {
		if _, err := svc.DeployMCPServer(ctx, "admin@x", in); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("case %d: want ErrBadRequest, got %v", i, err)
		}
	}
}

func TestMCPPortConflict(t *testing.T) {
	n := &fakeNomad{usedPorts: []int{8090}}
	svc, _ := newService(n, &fakeGateway{}, &fakeLLM{}, &fakeVault{})
	ctx := context.Background()
	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{Name: "clash-mcp", Image: "img", Transport: "sse", Port: 8090}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("want ErrConflict on used port, got %v", err)
	}
}

// ---- LLM flow ----

func TestLLMOnboardTestPublish(t *testing.T) {
	l := &fakeLLM{}
	v := &fakeVault{provider: map[string]string{"infra/llm-providers/deepseek#api_key": "sk-deepseek"}}
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, l, v)
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
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, &fakeLLM{}, v)

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
		svc, _ := newService(&fakeNomad{}, &fakeGateway{}, &fakeLLM{}, v)
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
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, l, v)
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
	svc, st := newService(&fakeNomad{}, &fakeGateway{}, l, v)
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
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, &fakeLLM{}, &fakeVault{provider: map[string]string{}})
	ctx := context.Background()
	// provider key not in Vault → upstream error (not a client-class apperr)
	if _, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "m", Provider: "nope", BackendModel: "x/y"}); err == nil {
		t.Fatalf("expected error when provider key is absent")
	}
}

// ---- credential blueprint flow ----

type fakeValidator struct{ pass bool }

func (f *fakeValidator) Validate(_ context.Context, m blueprint.BlueprintManifest) (blueprint.ValidationResult, error) {
	return blueprint.ValidationResult{Passed: f.pass}, nil
}

func newBlueprintSvc(t *testing.T, pass bool) (*Service, *fakeVault) {
	t.Helper()
	fv := &fakeVault{provider: map[string]string{}}
	svc := New(store.NewMemory(), &fakeNomad{}, &fakeGateway{}, &fakeLLM{}, fv, nil, Config{})
	svc.validator = &fakeValidator{pass: pass}
	return svc, fv
}

func TestCreateBlueprintDraft_StoresManifestOnRow(t *testing.T) {
	svc, fv := newBlueprintSvc(t, true)
	m := blueprint.BlueprintManifest{
		ID: "vault-mcp", Version: 1, Class: "C",
		PolicyTpl: `path "secret/data/projects/*" { capabilities = ["read"] }`,
		WIFRole:   blueprint.WIFRoleSpec{NameTpl: "mcp-vault-mcp", TokenTTL: "1h"},
	}
	bp, err := svc.CreateBlueprintDraft(context.Background(), "admin@x", m)
	if err != nil {
		t.Fatal(err)
	}
	if bp.Status != store.StatusDraft || bp.ContentHash != m.ContentHash() {
		t.Fatalf("draft not stored correctly: %+v", bp)
	}
	if len(bp.Manifest) == 0 {
		t.Fatal("manifest JSON must be persisted on the blueprint row")
	}
	if len(fv.written) != 0 {
		t.Fatalf("no Vault KV write expected for the blueprint manifest, got %v", fv.written)
	}
	// Validate must read the manifest back from the row — Vault stays untouched.
	vbp, err := svc.ValidateBlueprint(context.Background(), "admin@x", "vault-mcp", 1)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if vbp.Status != store.StatusValidated {
		t.Fatalf("expected validated, got %s", vbp.Status)
	}
	if len(fv.written) != 0 {
		t.Fatalf("validate must not touch Vault for the manifest, got %v", fv.written)
	}
}

func TestPublishBlueprint_RequiresValidatedFirst(t *testing.T) {
	svc, _ := newBlueprintSvc(t, true)
	m := blueprint.BlueprintManifest{
		ID: "vault-mcp", Version: 1, Class: "C",
		PolicyTpl: `path "secret/data/projects/*" { capabilities = ["read"] }`,
		WIFRole:   blueprint.WIFRoleSpec{NameTpl: "mcp-vault-mcp", TokenTTL: "1h"},
	}
	if _, err := svc.CreateBlueprintDraft(context.Background(), "admin@x", m); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishBlueprint(context.Background(), "admin@x", "vault-mcp", 1); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if _, err := svc.ValidateBlueprint(context.Background(), "admin@x", "vault-mcp", 1); err != nil {
		t.Fatal(err)
	}
	bp, err := svc.PublishBlueprint(context.Background(), "admin@x", "vault-mcp", 1)
	if err != nil {
		t.Fatal(err)
	}
	if bp.Status != store.StatusPublished {
		t.Fatalf("expected published, got %s", bp.Status)
	}
}

func TestDeleteBlueprint(t *testing.T) {
	svc, _ := newBlueprintSvc(t, true)
	ctx := context.Background()
	m := blueprint.BlueprintManifest{
		ID: "vault-mcp", Version: 1, Class: "C",
		PolicyTpl: `path "secret/data/projects/*" { capabilities = ["read"] }`,
		WIFRole:   blueprint.WIFRoleSpec{NameTpl: "mcp-vault-mcp", TokenTTL: "1h"},
	}

	// missing → ErrNotFound
	if err := svc.DeleteBlueprint(ctx, "admin@x", "vault-mcp", 1); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}

	// unreferenced → deletes OK and is then gone
	if _, err := svc.CreateBlueprintDraft(ctx, "admin@x", m); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteBlueprint(ctx, "admin@x", "vault-mcp", 1); err != nil {
		t.Fatalf("delete unreferenced: %v", err)
	}
	if _, err := svc.store.GetBlueprint(ctx, "vault-mcp", 1); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("blueprint should be gone after delete, got %v", err)
	}

	// bound by an MCP server type → ErrConflict and still present
	if _, err := svc.CreateBlueprintDraft(ctx, "admin@x", m); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.UpsertMCPServer(ctx, store.MCPServer{
		Name: "vault-mcp-srv", Image: "img", Transport: "sse", Port: 9400,
		BlueprintRef: &store.BlueprintRef{ID: "vault-mcp", Version: 1, ContentHash: m.ContentHash()},
	}); err != nil {
		t.Fatalf("seed bound server: %v", err)
	}
	if err := svc.DeleteBlueprint(ctx, "admin@x", "vault-mcp", 1); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("delete bound: want ErrConflict, got %v", err)
	}
	if _, err := svc.store.GetBlueprint(ctx, "vault-mcp", 1); err != nil {
		t.Fatalf("bound blueprint should still be present, got %v", err)
	}
}
