package projectadmin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

type fakeProjects struct{ ns string }

func (f fakeProjects) GetProject(_ context.Context, name string, _ []string) (descriptor.Descriptor, error) {
	return descriptor.Descriptor{ProjectName: name, Namespace: f.ns, DevelopersGroupName: name + "-developers"}, nil
}

type fakeExecutor struct {
	rec          blueprint.InstanceRecord
	instErr      error
	deprovisoned []blueprint.InstanceRecord
	gotGrants    []blueprint.PathGrant
}

func (f *fakeExecutor) Instantiate(_ context.Context, m blueprint.BlueprintManifest, ns string, _ map[string]string, grants []blueprint.PathGrant) (blueprint.InstanceRecord, error) {
	f.gotGrants = grants
	if f.instErr != nil {
		return blueprint.InstanceRecord{}, f.instErr
	}
	rec := f.rec
	rec.Namespace = ns
	rec.Ref = m.Ref()
	return rec, nil
}
func (f *fakeExecutor) Deprovision(_ context.Context, rec blueprint.InstanceRecord) error {
	f.deprovisoned = append(f.deprovisoned, rec)
	return nil
}

type fakeNomad struct {
	jobID      string
	regErr     error
	ipErr      error
	purged     []string
	existsResp bool
	lastHCL    string
}

func (f *fakeNomad) RegisterJob(_, jobHCL, _ string) (string, error) {
	f.lastHCL = jobHCL
	if f.regErr != nil {
		return "", f.regErr
	}
	if f.jobID == "" {
		f.jobID = "job-1"
	}
	return f.jobID, nil
}
func (f *fakeNomad) ResolvePlacementIP(_, _ string) (string, error) {
	if f.ipErr != nil {
		return "", f.ipErr
	}
	return "10.0.0.5", nil
}
func (f *fakeNomad) PurgeJob(_, jobID string) error      { f.purged = append(f.purged, jobID); return nil }
func (f *fakeNomad) JobExists(_, _ string) (bool, error) { return f.existsResp, nil }

type fakeVault struct{ manifest string }

func (f fakeVault) ReadKVField(_ context.Context, _, _ string) (string, error) {
	return f.manifest, nil
}

type fakeGateway struct {
	probe        mcpgw.ScopeProbe
	tools        []string
	registered   []string
	deletedPeers []string
	createdVS    []string
	deletedVS    []string
}

func (f *fakeGateway) RegisterPeer(_ context.Context, name, _ string) (string, error) {
	f.registered = append(f.registered, name)
	return "peer-" + name, nil
}
func (f *fakeGateway) DiscoverTools(context.Context, string) ([]string, error) { return f.tools, nil }
func (f *fakeGateway) CreateVirtualServer(_ context.Context, name, _ string, _ []string) (string, error) {
	f.createdVS = append(f.createdVS, name)
	return "vs-" + name, nil
}
func (f *fakeGateway) CreateScopedToken(context.Context, string, int, string) (string, error) {
	return "tok", nil
}
func (f *fakeGateway) RevokeTokensByPrefix(context.Context, string) error { return nil }
func (f *fakeGateway) ProbeScopedToken(context.Context, string, string, string) (mcpgw.ScopeProbe, error) {
	return f.probe, nil
}
func (f *fakeGateway) DeleteVirtualServer(_ context.Context, id string) error {
	f.deletedVS = append(f.deletedVS, id)
	return nil
}
func (f *fakeGateway) DeletePeer(_ context.Context, id string) error {
	f.deletedPeers = append(f.deletedPeers, id)
	return nil
}

func classAManifestJSON(t *testing.T) (string, string) {
	t.Helper()
	m := blueprint.BlueprintManifest{
		ID: "postgres-mcp", Version: 1, Class: blueprint.ClassA, Description: "pg",
		Engines:   []blueprint.EngineSpec{{Type: "database", Plugin: "postgresql-database-plugin", MountPathTpl: "database/{{.Namespace}}-pg"}},
		Role:      &blueprint.RoleSpec{NameTpl: "ro", CreationStatements: []string{"CREATE ROLE x;"}, DefaultTTLSeconds: 3600, MaxTTLSeconds: 7200},
		PolicyTpl: `path "{{.Mount}}/creds/{{.Role}}" { capabilities = ["read"] }`,
		WIFRole:   blueprint.WIFRoleSpec{NameTpl: "mcp-postgres-mcp", TokenTTL: "1h"},
		Params:    []blueprint.ParamSpec{{Name: "connection_url", Type: "string", Required: true}, {Name: "bootstrap_password", Type: "secret", Required: true}, {Name: "db_host", Type: "string", Required: true}, {Name: "db_port", Type: "string", Required: true}, {Name: "db_name", Type: "string", Required: true}},
		JobCredential: blueprint.JobCredentialSpec{EnvTemplates: map[string]string{
			"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}`,
		}},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(b), m.ContentHash()
}

func newService(t *testing.T, ex Executor, n NomadClient, g *fakeGateway, manifestJSON string) (*Service, store.Store) {
	t.Helper()
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, fakeVault{manifest: manifestJSON}, Config{})
	return svc, st
}

func TestListDeployable(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	svc, st := newService(t, &fakeExecutor{}, &fakeNomad{existsResp: true}, &fakeGateway{}, manifestJSON)
	ctx := context.Background()

	if _, err := st.UpsertMCPServer(ctx, store.MCPServer{Name: "postgres-mcp", Image: "img", Transport: "sse", Port: 9300, Status: store.StatusPublished, BlueprintRef: &store.BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: hash}}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	if _, err := st.UpsertMCPServer(ctx, store.MCPServer{Name: "vault-mcp", Image: "img", Transport: "sse", Port: 9301, Status: store.StatusPublished}); err != nil {
		t.Fatalf("seed type2: %v", err)
	}
	if _, err := st.UpsertBlueprint(ctx, store.Blueprint{ID: "postgres-mcp", Version: 1, Class: "A", ContentHash: hash, Status: store.StatusPublished}); err != nil {
		t.Fatalf("seed blueprint: %v", err)
	}

	cat, err := svc.ListDeployable(ctx, []string{"project-acme-developers"}, "project-acme")
	if err != nil {
		t.Fatalf("ListDeployable: %v", err)
	}
	if len(cat.Deployable) != 1 || cat.Deployable[0].Name != "postgres-mcp" {
		t.Fatalf("deployable: %+v", cat.Deployable)
	}
	if len(cat.Deployed) != 0 {
		t.Fatalf("deployed should be empty: %+v", cat.Deployed)
	}
}

func seedDeployable(t *testing.T, st store.Store, hash string) {
	t.Helper()
	if _, err := st.UpsertMCPServer(context.Background(), store.MCPServer{
		Name: "postgres-mcp", Image: "ghcr.io/x/postgres-mcp:1", Transport: "streamable-http", Port: 9300,
		Status: store.StatusPublished, BlueprintRef: &store.BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: hash},
	}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	if _, err := st.UpsertBlueprint(context.Background(), store.Blueprint{ID: "postgres-mcp", Version: 1, Class: "A", ContentHash: hash, Status: store.StatusPublished}); err != nil {
		t.Fatalf("seed blueprint: %v", err)
	}
}

func deployParams() map[string]string {
	return map[string]string{"connection_url": "postgresql://demo-db", "bootstrap_password": "boot", "db_host": "demo-db", "db_port": "5432", "db_name": "app"}
}

func blueprintInstance() blueprint.InstanceRecord {
	return blueprint.InstanceRecord{
		WIFRoleName: "mcp-postgres-mcp", Mounts: []string{"database/project-acme-pg"},
		LeasePrefixes: []string{"database/project-acme-pg/creds/ro"}, PolicyNames: []string{"mcp-postgres-mcp"},
	}
}

func TestDeployServerHappyPath(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	n := &fakeNomad{}
	svc, st := newService(t, ex, n, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)
	ctx := context.Background()

	saved, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "postgres-mcp", Params: deployParams()})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if saved.Status != store.StatusDeployed || saved.JobID == "" || saved.BlueprintRef.ContentHash != hash {
		t.Fatalf("saved row wrong: %+v", saved)
	}
	if len(saved.Instance) == 0 {
		t.Fatalf("instance record not persisted")
	}
	if want := `namespace = "project-acme"`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing %q:\n%s", want, n.lastHCL)
	}
	if want := `role      = "mcp-postgres-mcp"`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing WIF role:\n%s", n.lastHCL)
	}
	if want := `DATABASE_URI={{ with secret "database/project-acme-pg/creds/ro" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@demo-db:5432/app{{ end }}`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing rendered credential env:\n%s", n.lastHCL)
	}

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "postgres-mcp", Params: deployParams()}); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate: want ErrConflict, got %v", err)
	}

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "nope", Params: deployParams()}); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown type: want ErrNotFound, got %v", err)
	}
}

func TestDeployServerRollsBackOnRegisterFailure(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	n := &fakeNomad{regErr: errors.New("nomad down")}
	svc, st := newService(t, ex, n, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)
	ctx := context.Background()

	_, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "postgres-mcp", Params: deployParams()})
	if err == nil {
		t.Fatalf("expected deploy error")
	}
	if len(ex.deprovisoned) != 1 {
		t.Fatalf("expected exactly one Deprovision, got %d", len(ex.deprovisoned))
	}
	if ex.deprovisoned[0].WIFRoleName != "mcp-postgres-mcp" {
		t.Fatalf("deprovisioned the wrong record: %+v", ex.deprovisoned[0])
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row should not exist: %v", err)
	}
}

// A failure AFTER the Nomad job is registered (here: resolve-placement) must both
// purge the orphan job and deprovision the Vault instance — exactly once each — and
// persist no row.
func TestDeployServerRollsBackAndPurgesOnPostRegisterFailure(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	n := &fakeNomad{ipErr: errors.New("no placement")}
	svc, st := newService(t, ex, n, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)
	ctx := context.Background()

	_, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "postgres-mcp", Params: deployParams()})
	if err == nil {
		t.Fatalf("expected deploy error")
	}
	if len(ex.deprovisoned) != 1 {
		t.Fatalf("expected exactly one Deprovision, got %d", len(ex.deprovisoned))
	}
	if len(n.purged) != 1 {
		t.Fatalf("expected the registered job to be purged once, got %v", n.purged)
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row should not exist: %v", err)
	}
}

func TestDeleteServer(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1"}}
	n := &fakeNomad{}
	svc, st := newService(t, ex, n, g, manifestJSON)
	seedDeployable(t, st, hash)
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", DeployInput{ServerType: "postgres-mcp", Params: deployParams()}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := svc.TestServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp"); err != nil {
		t.Fatalf("test: %v", err)
	}

	if err := svc.DeleteServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(ex.deprovisoned) != 1 || ex.deprovisoned[0].WIFRoleName != "mcp-postgres-mcp" {
		t.Fatalf("deprovision not called with persisted record: %+v", ex.deprovisoned)
	}
	if len(n.purged) != 1 {
		t.Fatalf("nomad job not purged: %+v", n.purged)
	}
	if len(g.deletedPeers) != 1 {
		t.Fatalf("peer not deregistered: %+v", g.deletedPeers)
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row not dropped: %v", err)
	}
	if err := svc.DeleteServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown: want ErrNotFound, got %v", err)
	}
}

func passingProbe() mcpgw.ScopeProbe {
	return mcpgw.ScopeProbe{OwnServerOK: true, AdminDenied: true, OtherServerDenied: true, OtherServerChecked: true}
}

func TestTestServer(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1", "t2"}}
	svc, st := newService(t, ex, &fakeNomad{}, g, manifestJSON)
	seedDeployable(t, st, hash)
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", DeployInput{ServerType: "postgres-mcp", Params: deployParams()}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	got, err := svc.TestServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp")
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got.TestResult == nil || !got.TestResult.Passed || got.TestResult.ToolsDiscovered != 2 {
		t.Fatalf("test result: %+v", got.TestResult)
	}
	if got.PeerID == "" {
		t.Fatalf("peer id not recorded")
	}
	if len(g.deletedVS) != 2 {
		t.Fatalf("temp virtual servers not torn down: %+v", g.deletedVS)
	}
	if len(g.deletedPeers) != 0 {
		t.Fatalf("peer must be kept after test")
	}
	if _, err := svc.TestServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown: want ErrNotFound, got %v", err)
	}
}

func TestDeployServer_ThreadsExtraGrants(t *testing.T) {
	manifestJSON, hash := classAManifestJSON(t)
	ex := &fakeExecutor{rec: blueprintInstance()}
	svc, st := newService(t, ex, &fakeNomad{}, &fakeGateway{}, manifestJSON)
	seedDeployable(t, st, hash)

	grants := []blueprint.PathGrant{{Path: "pki/project-acme/issue/web", Capabilities: []string{"create", "update"}}}
	if _, err := svc.DeployServer(context.Background(), "acme-admin@x", []string{"project-acme-developers"}, "project-acme",
		DeployInput{ServerType: "postgres-mcp", Params: deployParams(), ExtraGrants: grants}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(ex.gotGrants) != 1 || ex.gotGrants[0].Path != "pki/project-acme/issue/web" {
		t.Fatalf("ExtraGrants not threaded to executor: %+v", ex.gotGrants)
	}
}
