package agents

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// ---- fakes ----

type fakeProjects struct {
	ns  string
	err error
}

func (f fakeProjects) GetProject(context.Context, string, []string) (descriptor.Descriptor, error) {
	if f.err != nil {
		return descriptor.Descriptor{}, f.err
	}
	return descriptor.Descriptor{ProjectName: "project-acme", Namespace: f.ns}, nil
}

type fakeNomad struct {
	lastHCL     string
	jobID       string
	ip          string
	port        int
	resolveErr  error
	registerErr error
	purged      []string
	exists      bool
}

func (f *fakeNomad) RegisterJob(_, hcl, _ string) (string, error) {
	f.lastHCL = hcl
	if f.registerErr != nil {
		return "", f.registerErr
	}
	return f.jobID, nil
}
func (f *fakeNomad) ResolvePlacement(_, _ string) (string, int, error) {
	return f.ip, f.port, f.resolveErr
}
func (f *fakeNomad) PurgeJob(_, jobID string) error      { f.purged = append(f.purged, jobID); return nil }
func (f *fakeNomad) JobExists(_, _ string) (bool, error) { return f.exists, nil }

type fakeVault struct {
	policies   map[string]string
	wifRoles   map[string]blueprint.WIFRole
	kv         map[string]map[string]any
	kvDelErr   error
	kvDeleted  []string
	writeKVErr error
}

func newFakeVault() *fakeVault {
	return &fakeVault{policies: map[string]string{}, wifRoles: map[string]blueprint.WIFRole{}, kv: map[string]map[string]any{}}
}
func (f *fakeVault) WritePolicy(_ context.Context, _, name, hcl string) error {
	f.policies[name] = hcl
	return nil
}
func (f *fakeVault) WriteWIFRole(_ context.Context, _, _, roleName string, role blueprint.WIFRole) error {
	f.wifRoles[roleName] = role
	return nil
}
func (f *fakeVault) WriteKVv2(_ context.Context, _, _, relPath string, data map[string]any) error {
	if f.writeKVErr != nil {
		return f.writeKVErr
	}
	f.kv[relPath] = data
	return nil
}
func (f *fakeVault) DeleteKVv2Metadata(_ context.Context, _, _, relPath string) error {
	f.kvDeleted = append(f.kvDeleted, relPath)
	delete(f.kv, relPath)
	return f.kvDelErr
}

type fakeLLM struct {
	generated map[string]string // alias -> key
	genErr    error
	deleted   []string
	nextKey   string
}

func newFakeLLM() *fakeLLM { return &fakeLLM{generated: map[string]string{}, nextKey: "sk-agent-1"} }
func (f *fakeLLM) GenerateKey(_ context.Context, spec llmgw.KeySpec) (string, error) {
	if f.genErr != nil {
		return "", f.genErr
	}
	f.generated[spec.Alias] = f.nextKey
	return f.nextKey, nil
}
func (f *fakeLLM) DeleteKeyByAlias(_ context.Context, alias string) error {
	f.deleted = append(f.deleted, alias)
	delete(f.generated, alias)
	return nil
}

type fakeHTTP struct {
	resp    *http.Response
	err     error
	calls   int
	failFor int // return err for the first N calls, then resp
}

func (f *fakeHTTP) Do(*http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failFor {
		return nil, fmt.Errorf("dial tcp: connection refused")
	}
	return f.resp, f.err
}

func bodyResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// fakeGateway is a minimal mcpgw.Client covering the template-wiring calls.
type fakeGateway struct {
	tools       []mcpgw.ToolInfo
	createdVS   []string   // virtual-server names, in order
	vsToolIDs   [][]string // tool ids passed to each CreateVirtualServer
	revoked     []string
	deletedVS   []string
	createVSErr error
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{tools: []mcpgw.ToolInfo{{ID: "tid-search", Name: "search"}, {ID: "tid-fetch", Name: "fetch"}}}
}
func (g *fakeGateway) RegisterPeer(context.Context, string, string, string) (string, error) {
	return "peer", nil
}
func (g *fakeGateway) UpdatePeer(context.Context, string, string, string, string) error { return nil }
func (g *fakeGateway) ActivatePeer(context.Context, string) error                       { return nil }
func (g *fakeGateway) DiscoverTools(context.Context, string) ([]string, error) {
	ids := []string{}
	for _, t := range g.tools {
		ids = append(ids, t.ID)
	}
	return ids, nil
}
func (g *fakeGateway) ListTools(context.Context, string) ([]mcpgw.ToolInfo, error) {
	return g.tools, nil
}
func (g *fakeGateway) CreateVirtualServer(_ context.Context, name, _ string, toolIDs []string) (string, error) {
	if g.createVSErr != nil {
		return "", g.createVSErr
	}
	g.createdVS = append(g.createdVS, name)
	g.vsToolIDs = append(g.vsToolIDs, toolIDs)
	return "vs-" + name, nil
}
func (g *fakeGateway) CreateScopedToken(context.Context, string, int, string) (string, error) {
	return "scoped-tok", nil
}
func (g *fakeGateway) RevokeTokensByPrefix(_ context.Context, prefix string) error {
	g.revoked = append(g.revoked, prefix)
	return nil
}
func (g *fakeGateway) ProbeScopedToken(context.Context, string, string, string) (mcpgw.ScopeProbe, error) {
	return mcpgw.ScopeProbe{}, nil
}
func (g *fakeGateway) DeleteVirtualServer(_ context.Context, id string) error {
	g.deletedVS = append(g.deletedVS, id)
	return nil
}
func (g *fakeGateway) DeleteVirtualServerByName(context.Context, string) error { return nil }
func (g *fakeGateway) DeletePeer(context.Context, string) error                { return nil }

// ---- helpers ----

func newService(t *testing.T, st store.Store, nomad *fakeNomad, vault *fakeVault, llm *fakeLLM, gw mcpgw.Client, httpDoer HTTPDoer) *Service {
	t.Helper()
	return New(st, fakeProjects{ns: "project-acme"}, nomad, vault, llm, gw, httpDoer, Config{
		NodePool: "agents", Image: "panchalravi/agent-runtime:agentv1",
		LLMModels: []string{"deepseek-v4-pro"}, LLMMaxBudget: 25, LLMRPMLimit: 60,
		LLMGatewayPrivateEndpoint: "http://10.0.0.9:4000/v1", MCPGatewayEndpoint: "http://10.0.0.8:4444",
		VaultBoundAudience: "vault",
	})
}

// deployAServer seeds a tested MCP server (peer + tool catalog) so a template can
// wire a tool subset against it.
func deployAServer(t *testing.T, st store.Store) {
	t.Helper()
	_, err := st.UpsertProjectMCPServer(context.Background(), store.ProjectMCPServer{
		Project: "project-acme", Name: "github", Status: store.StatusDeployed, PeerID: "peer-github",
		Tools: []store.MCPTool{{ID: "tid-search", Name: "search"}, {ID: "tid-fetch", Name: "fetch"}},
	})
	if err != nil {
		t.Fatalf("seed mcp server: %v", err)
	}
}

// saveDeployTest is the common front half of most tests: author a draft then run the
// admin test deploy, returning the deployed row.
func saveDeployTest(t *testing.T, svc *Service, src string) store.ProjectAgentTemplate {
	t.Helper()
	if _, err := svc.Save(context.Background(), "adm@x", nil, "project-acme", src); err != nil {
		t.Fatalf("save: %v", err)
	}
	row, err := svc.DeployTest(context.Background(), "adm@x", nil, "project-acme", "support-triage")
	if err != nil {
		t.Fatalf("deploy-test: %v", err)
	}
	return row
}

// ---- tests ----

func TestSaveCreatesDraftAndBumpsVersion(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	svc := newService(t, st, &fakeNomad{}, newFakeVault(), newFakeLLM(), newFakeGateway(), &fakeHTTP{})

	saved, err := svc.Save(context.Background(), "adm@x", nil, "project-acme", goodYAML)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Status != statusDraft || saved.Version != 1 || saved.Model != "deepseek-v4-pro" {
		t.Fatalf("draft row: %+v", saved)
	}
	again, err := svc.Save(context.Background(), "adm@x", nil, "project-acme", goodYAML)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if again.Version != 2 || again.Status != statusDraft {
		t.Fatalf("re-save row: %+v", again)
	}
}

func TestDeployTestWiresToolSubset(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "agent-project-acme-tmpl-support-triage", ip: "10.0.0.5", port: 27000}
	vault := newFakeVault()
	llm := newFakeLLM()
	gw := newFakeGateway()
	svc := newService(t, st, nomad, vault, llm, gw, &fakeHTTP{})

	// Select only "search" of github's two tools.
	src := strings.Replace(goodYAML, "  mcp_servers: [github]", "  mcp_servers:\n    - {server: github, tools: [search]}", 1)
	row := saveDeployTest(t, svc, src)

	if row.Status != statusDraft || row.Endpoint != "10.0.0.5:27000" || row.LLMKeyAlias != "llm-project-acme-tmpl-support-triage" {
		t.Fatalf("deployed row: %+v", row)
	}
	// Virtual server scoped to exactly the selected tool id.
	if len(gw.createdVS) != 1 || gw.createdVS[0] != "tmpl-project-acme-support-triage-github" {
		t.Fatalf("virtual server name: %v", gw.createdVS)
	}
	if len(gw.vsToolIDs) != 1 || len(gw.vsToolIDs[0]) != 1 || gw.vsToolIDs[0][0] != "tid-search" {
		t.Fatalf("subset not scoped to [tid-search]: %v", gw.vsToolIDs)
	}
	// Per-template MCP KV + LLM KV written.
	if vault.kv["projects/agents/tmpl-support-triage/mcp/github"]["token"] != "scoped-tok" {
		t.Fatalf("template mcp KV: %+v", vault.kv)
	}
	if vault.kv["projects/agents/tmpl-support-triage"]["virtual_key"] != "sk-agent-1" {
		t.Fatalf("template LLM KV: %+v", vault.kv)
	}
	// Rendered HCL points at the agents role + the per-template MCP KV path.
	for _, want := range []string{
		`role      = "agents"`,
		`secret/data/projects/agents/tmpl-support-triage/mcp/github`,
		`secret/data/projects/agents/tmpl-support-triage"`,
	} {
		if !strings.Contains(nomad.lastHCL, want) {
			t.Fatalf("HCL missing %q", want)
		}
	}
	// Wiring recorded for teardown.
	if len(row.Wiring) != 1 || row.Wiring[0].VirtualServerID != "vs-tmpl-project-acme-support-triage-github" {
		t.Fatalf("wiring: %+v", row.Wiring)
	}
}

// A whole-server selection (bare name) scopes the virtual server to all tool ids.
func TestDeployTestWholeServer(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	gw := newFakeGateway()
	svc := newService(t, st, &fakeNomad{jobID: "j", ip: "1.2.3.4", port: 100}, newFakeVault(), newFakeLLM(), gw, &fakeHTTP{})
	saveDeployTest(t, svc, goodYAML)
	if len(gw.vsToolIDs) != 1 || len(gw.vsToolIDs[0]) != 2 {
		t.Fatalf("whole-server must include all tool ids: %v", gw.vsToolIDs)
	}
}

// A failure after the first wiring/key mutation must tear down the virtual server,
// the LiteLLM key, the KV, and purge the job so a deploy never leaves orphan state.
func TestDeployTestCleansUpOnPlacementFailure(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "j1", resolveErr: fmt.Errorf("no allocation")}
	vault := newFakeVault()
	llm := newFakeLLM()
	gw := newFakeGateway()
	svc := newService(t, st, nomad, vault, llm, gw, &fakeHTTP{})

	if _, err := svc.Save(context.Background(), "adm@x", nil, "project-acme", goodYAML); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := svc.DeployTest(context.Background(), "adm@x", nil, "project-acme", "support-triage"); err == nil {
		t.Fatalf("deploy-test must fail on placement error")
	}
	if len(gw.deletedVS) == 0 {
		t.Fatalf("virtual server not torn down: %v", gw.deletedVS)
	}
	if len(llm.deleted) == 0 || len(vault.kvDeleted) == 0 || len(nomad.purged) == 0 {
		t.Fatalf("cleanup incomplete: key=%v kv=%v purged=%v", llm.deleted, vault.kvDeleted, nomad.purged)
	}
}

func TestTestAdvancesToTested(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "agent-project-acme-tmpl-support-triage", ip: "10.0.0.5", port: 27000}
	hz := &fakeHTTP{resp: bodyResp(200, `{"status":"ok","agent":"support-triage","tools":["search"]}`)}
	svc := newService(t, st, nomad, newFakeVault(), newFakeLLM(), newFakeGateway(), hz)
	saveDeployTest(t, svc, goodYAML)

	saved, err := svc.Test(context.Background(), "adm@x", nil, "project-acme", "support-triage")
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if saved.Status != statusTested || saved.TestResult == nil || !saved.TestResult.Passed || saved.TestResult.ToolsDiscovered != 1 {
		t.Fatalf("test result: status=%s %+v", saved.Status, saved.TestResult)
	}
}

func TestPublishRequiresTested(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "j", ip: "1.2.3.4", port: 100}
	hz := &fakeHTTP{resp: bodyResp(200, `{"status":"ok","tools":["search"]}`)}
	svc := newService(t, st, nomad, newFakeVault(), newFakeLLM(), newFakeGateway(), hz)
	saveDeployTest(t, svc, goodYAML)

	// Not yet tested → publish is a conflict.
	if _, err := svc.Publish(context.Background(), "adm@x", nil, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("publish before test: want ErrConflict, got %v", err)
	}
	if _, err := svc.Test(context.Background(), "adm@x", nil, "project-acme", "support-triage"); err != nil {
		t.Fatalf("test: %v", err)
	}
	pub, err := svc.Publish(context.Background(), "adm@x", nil, "project-acme", "support-triage")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != statusPublished {
		t.Fatalf("status not published: %+v", pub)
	}
}

func TestDeleteTemplateTearsDown(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "agent-project-acme-tmpl-support-triage", ip: "10.0.0.5", port: 27000}
	vault := newFakeVault()
	llm := newFakeLLM()
	gw := newFakeGateway()
	svc := newService(t, st, nomad, vault, llm, gw, &fakeHTTP{})
	saveDeployTest(t, svc, goodYAML)

	if err := svc.DeleteTemplate(context.Background(), "adm@x", nil, "project-acme", "support-triage"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(nomad.purged) == 0 || len(llm.deleted) == 0 || len(gw.deletedVS) == 0 {
		t.Fatalf("delete must purge job + key + virtual server: purged=%v deleted=%v vs=%v", nomad.purged, llm.deleted, gw.deletedVS)
	}
	if _, err := st.GetProjectAgentTemplate(context.Background(), "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row must be gone, got %v", err)
	}
}

func TestValidateYAMLUndeployedServer(t *testing.T) {
	st := store.NewMemory() // no MCP server seeded
	svc := newService(t, st, &fakeNomad{}, newFakeVault(), newFakeLLM(), newFakeGateway(), &fakeHTTP{})
	if err := svc.ValidateYAML(context.Background(), nil, "project-acme", goodYAML); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("undeployed server: want ErrBadRequest, got %v", err)
	}
}

// ChatTest retries once against a re-resolved endpoint when the first dial fails.
func TestChatTestRetriesOnDialFailure(t *testing.T) {
	st := store.NewMemory()
	deployAServer(t, st)
	nomad := &fakeNomad{jobID: "agent-project-acme-tmpl-support-triage", ip: "10.0.0.5", port: 27000}
	http1 := &fakeHTTP{resp: bodyResp(200, "event: token\ndata: {\"text\":\"hi\"}\n\n"), failFor: 1}
	svc := newService(t, st, nomad, newFakeVault(), newFakeLLM(), newFakeGateway(), http1)
	saveDeployTest(t, svc, goodYAML)

	nomad.ip, nomad.port = "10.0.0.7", 28000
	resp, err := svc.ChatTest(context.Background(), nil, "project-acme", "support-triage", strings.NewReader(`{"message":"hi","thread_id":"t1"}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if http1.calls != 2 {
		t.Fatalf("expected 2 dials (fail + retry), got %d", http1.calls)
	}
	row, _ := st.GetProjectAgentTemplate(context.Background(), "project-acme", "support-triage")
	if row.Endpoint != "10.0.0.7:28000" {
		t.Fatalf("endpoint not refreshed on retry: %s", row.Endpoint)
	}
	resp.Body.Close()
}
