package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
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
	probe       mcpgw.ScopeProbe
	tools       []string
	deletedPeer bool
}

func (f *fakeGateway) RegisterPeer(_ context.Context, name, url string) (string, error) {
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
		out = append(out, llmgw.Model{Name: a.ModelName, ID: "id-" + a.ModelName, Source: "db"})
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
	return New(st, n, g, l, v, Config{MCPJobVaultRole: "infra-mcp-job"}), st
}

// ---- MCP flow ----

func TestMCPDeployTestPublish(t *testing.T) {
	n := &fakeNomad{}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1", "t2"}}
	v := &fakeVault{provider: map[string]string{}}
	svc, _ := newService(n, g, &fakeLLM{}, v)
	ctx := context.Background()

	in := DeployMCPInput{Name: "vault-mcp", Image: "hashicorp/vault-mcp-server", Transport: "sse", Port: 9100}
	srv, err := svc.DeployMCPServer(ctx, "admin@x", in)
	if err != nil {
		t.Fatalf("DeployMCPServer: %v", err)
	}
	if srv.Status != store.StatusDeployed || srv.JobID != "jobid" {
		t.Fatalf("unexpected deploy state: %+v", srv)
	}
	if srv.GatewayURL != "http://10.0.0.5:9100/sse" {
		t.Fatalf("GatewayURL = %q", srv.GatewayURL)
	}
	if !strings.Contains(n.lastHCL, "image      = \"hashicorp/vault-mcp-server\"") {
		t.Fatalf("rendered HCL missing image:\n%s", n.lastHCL)
	}

	// publish before a green test is rejected
	if _, err := svc.PublishMCPServer(ctx, "admin@x", "vault-mcp"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("publish before test: want ErrConflict, got %v", err)
	}

	tested, err := svc.TestMCPServer(ctx, "admin@x", "vault-mcp")
	if err != nil {
		t.Fatalf("TestMCPServer: %v", err)
	}
	if tested.TestResult == nil || !tested.TestResult.Passed || tested.TestResult.ToolsDiscovered != 2 {
		t.Fatalf("unexpected test result: %+v", tested.TestResult)
	}

	pub, err := svc.PublishMCPServer(ctx, "admin@x", "vault-mcp")
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

func TestMCPFailingProbeBlocksPublish(t *testing.T) {
	n := &fakeNomad{}
	g := &fakeGateway{probe: mcpgw.ScopeProbe{OwnServerOK: true, AdminDenied: false}, tools: []string{"t1"}}
	svc, _ := newService(n, g, &fakeLLM{}, &fakeVault{})
	ctx := context.Background()

	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{Name: "bad-mcp", Image: "img", Transport: "streamable-http", Port: 9200}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	tested, _ := svc.TestMCPServer(ctx, "admin@x", "bad-mcp")
	if tested.TestResult.Passed {
		t.Fatalf("test should fail when admin access is not denied")
	}
	if _, err := svc.PublishMCPServer(ctx, "admin@x", "bad-mcp"); !errors.Is(err, apperr.ErrConflict) {
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
	}
	for i, in := range cases {
		if _, err := svc.DeployMCPServer(ctx, "admin@x", in); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("case %d: want ErrBadRequest, got %v", i, err)
		}
	}
}

func TestMCPPortConflict(t *testing.T) {
	n := &fakeNomad{usedPorts: []int{9300}}
	svc, _ := newService(n, &fakeGateway{}, &fakeLLM{}, &fakeVault{})
	ctx := context.Background()
	if _, err := svc.DeployMCPServer(ctx, "admin@x", DeployMCPInput{Name: "clash-mcp", Image: "img", Transport: "sse", Port: 9300}); !errors.Is(err, apperr.ErrConflict) {
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

func TestLLMOnboardMissingProviderKey(t *testing.T) {
	svc, _ := newService(&fakeNomad{}, &fakeGateway{}, &fakeLLM{}, &fakeVault{provider: map[string]string{}})
	ctx := context.Background()
	// provider key not in Vault → upstream error (not a client-class apperr)
	if _, err := svc.OnboardLLMModel(ctx, "admin@x", OnboardLLMInput{Name: "m", Provider: "nope", BackendModel: "x/y"}); err == nil {
		t.Fatalf("expected error when provider key is absent")
	}
}
