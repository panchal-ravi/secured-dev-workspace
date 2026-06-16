package projectadmin

import (
	"context"
	"encoding/json"
	"testing"

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
}

func (f *fakeExecutor) Instantiate(_ context.Context, m blueprint.BlueprintManifest, ns string, _ map[string]string) (blueprint.InstanceRecord, error) {
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

	cat, err := svc.ListDeployable(ctx, "project-acme")
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
