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
	instantiated []string // server names
	gotSpec      blueprint.CredentialSpec
	gotParams    map[string]string
	deprovisoned []blueprint.InstanceRecord
	gotGrants    []blueprint.PathGrant
	updated      []string // server names passed to UpdateGrants
	updateErr    error
}

func (f *fakeExecutor) Instantiate(_ context.Context, serverName string, spec blueprint.CredentialSpec, ns string, params map[string]string, grants []blueprint.PathGrant) (blueprint.InstanceRecord, error) {
	f.instantiated = append(f.instantiated, serverName)
	f.gotSpec = spec
	f.gotParams = params
	f.gotGrants = grants
	if f.instErr != nil {
		return blueprint.InstanceRecord{}, f.instErr
	}
	rec := f.rec
	rec.Namespace = ns
	return rec, nil
}
func (f *fakeExecutor) UpdateGrants(_ context.Context, rec blueprint.InstanceRecord, spec blueprint.CredentialSpec, serverName string, grants []blueprint.PathGrant) (blueprint.InstanceRecord, error) {
	f.updated = append(f.updated, serverName)
	f.gotSpec = spec
	f.gotGrants = grants
	if f.updateErr != nil {
		return rec, f.updateErr
	}
	rec.ExtraGrants = grants
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
	regCount   int
}

func (f *fakeNomad) RegisterJob(_, jobHCL, _ string) (string, error) {
	f.lastHCL = jobHCL
	f.regCount++
	if f.regErr != nil {
		return "", f.regErr
	}
	if f.jobID == "" {
		f.jobID = "job-1"
	}
	return f.jobID, nil
}
func (f *fakeNomad) ResolvePlacement(_, _ string) (string, int, error) {
	if f.ipErr != nil {
		return "", 0, f.ipErr
	}
	return "10.0.0.5", 9100, nil
}
func (f *fakeNomad) PurgeJob(_, jobID string) error      { f.purged = append(f.purged, jobID); return nil }
func (f *fakeNomad) JobExists(_, _ string) (bool, error) { return f.existsResp, nil }

type fakeGateway struct {
	probe          mcpgw.ScopeProbe
	tools          []string
	toolInfos      []mcpgw.ToolInfo
	registered     []string
	updatedPeers   []string // "peerID url transport" per UpdatePeer call
	updatePeerErr  error
	activatedPeers []string // peerIDs per ActivatePeer call
	deletedPeers   []string
	createdVS      []string
	deletedVS      []string
	deletedVSNames []string
	revoked        []string // RevokeTokensByPrefix prefixes, in order
}

func (f *fakeGateway) RegisterPeer(_ context.Context, name, _, _ string) (string, error) {
	f.registered = append(f.registered, name)
	return "peer-" + name, nil
}
func (f *fakeGateway) UpdatePeer(_ context.Context, peerID, _, url, transport string) error {
	if f.updatePeerErr != nil {
		return f.updatePeerErr
	}
	f.updatedPeers = append(f.updatedPeers, peerID+" "+url+" "+transport)
	return nil
}
func (f *fakeGateway) ActivatePeer(_ context.Context, peerID string) error {
	f.activatedPeers = append(f.activatedPeers, peerID)
	return nil
}
func (f *fakeGateway) DiscoverTools(context.Context, string) ([]string, error) { return f.tools, nil }
func (f *fakeGateway) ListTools(context.Context, string) ([]mcpgw.ToolInfo, error) {
	return f.toolInfos, nil
}
func (f *fakeGateway) CreateVirtualServer(_ context.Context, name, _ string, _ []string) (string, error) {
	f.createdVS = append(f.createdVS, name)
	return "vs-" + name, nil
}
func (f *fakeGateway) CreateScopedToken(context.Context, string, int, string) (string, error) {
	return "tok", nil
}
func (f *fakeGateway) RevokeTokensByPrefix(_ context.Context, prefix string) error {
	f.revoked = append(f.revoked, prefix)
	return nil
}
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
func (f *fakeGateway) DeleteVirtualServerByName(_ context.Context, name string) error {
	f.deletedVSNames = append(f.deletedVSNames, name)
	return nil
}

type fakeWirer struct {
	wired []string // "<project>/<server>" per call
	err   error
}

func (f *fakeWirer) WireMCPServer(_ context.Context, project, serverName string) error {
	f.wired = append(f.wired, project+"/"+serverName)
	return f.err
}

func newService(t *testing.T, ex Executor, n NomadClient, g *fakeGateway) (*Service, store.Store) {
	t.Helper()
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, nil, Config{})
	return svc, st
}

// updFrom builds the full-replacement UpdateInput from a deploy input's
// container definition (the frontend always resends the whole definition).
func updFrom(in DeployInput, env map[string]string, grants []blueprint.PathGrant) UpdateInput {
	return UpdateInput{
		Image: in.Image, Command: in.Command, Transport: in.Transport, Port: in.Port, Path: in.Path,
		Env: env, ExtraGrants: grants,
	}
}

// pgInput mirrors the frontend PostgreSQL preset: full server definition +
// dynamic credential spec in one wizard payload.
func pgInput() DeployInput {
	return DeployInput{
		Name:      "postgres-mcp",
		Image:     "ghcr.io/x/postgres-mcp:1",
		Transport: "streamable-http",
		Port:      8080,
		Credential: blueprint.CredentialSpec{
			Source: blueprint.SourceDynamic,
			Params: []blueprint.ParamSpec{
				{Name: "connection_url", Type: "string", Required: true},
				{Name: "bootstrap_password", Type: "secret", Required: true},
				{Name: "db_host", Type: "string", Required: true},
				{Name: "db_port", Type: "string", Required: true},
				{Name: "db_name", Type: "string", Required: true},
			},
			Dynamic: &blueprint.DynamicSpec{
				Engine: "database",
				Mount:  "database/postgres-mcp",
				Configs: []blueprint.LogicalWrite{{Path: "config/conn", Data: map[string]any{
					"plugin_name": "postgresql-database-plugin", "connection_url": "${connection_url}",
				}}},
				RotateRootPath: "rotate-root/conn",
				Role:           &blueprint.LogicalWrite{Path: "roles/mcp-ro", Data: map[string]any{"db_name": "conn"}},
				CredsPath:      "creds/mcp-ro",
			},
			EnvTemplates: map[string]string{
				"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}`,
			},
		},
		Params: deployParams(),
	}
}

func deployParams() map[string]string {
	return map[string]string{"connection_url": "postgresql://demo-db", "bootstrap_password": "s3cr3t-value-9", "db_host": "demo-db", "db_port": "5432", "db_name": "app"}
}

func instanceRecord() blueprint.InstanceRecord {
	return blueprint.InstanceRecord{
		WIFRoleName: "mcp-postgres-mcp", Mounts: []string{"database/postgres-mcp"},
		LeasePrefixes: []string{"database/postgres-mcp/creds/mcp-ro"}, PolicyNames: []string{"mcp-postgres-mcp"},
		CredPath: "database/postgres-mcp/creds/mcp-ro",
	}
}

func TestDeployServerHappyPath(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	svc, _ := newService(t, ex, n, &fakeGateway{})
	ctx := context.Background()

	saved, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput())
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if saved.Status != store.StatusDeployed || saved.JobID == "" {
		t.Fatalf("saved row wrong: %+v", saved)
	}
	if saved.Image != "ghcr.io/x/postgres-mcp:1" || saved.Port != 8080 || saved.Transport != "streamable-http" {
		t.Fatalf("server definition not persisted: %+v", saved)
	}
	if saved.BlueprintRef != nil {
		t.Fatalf("wizard rows carry no blueprint ref: %+v", saved.BlueprintRef)
	}
	var specOnRow blueprint.CredentialSpec
	if err := json.Unmarshal(saved.Credential, &specOnRow); err != nil || specOnRow.Source != blueprint.SourceDynamic {
		t.Fatalf("credential spec not persisted: %v %+v", err, specOnRow)
	}
	if len(saved.Instance) == 0 {
		t.Fatalf("instance record not persisted")
	}
	if ex.instantiated[0] != "postgres-mcp" {
		t.Fatalf("executor got server name %q", ex.instantiated[0])
	}
	if want := `namespace = "project-acme"`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing %q:\n%s", want, n.lastHCL)
	}
	if want := `role      = "mcp-postgres-mcp"`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing WIF role:\n%s", n.lastHCL)
	}
	if want := `DATABASE_URI={{ with secret "database/postgres-mcp/creds/mcp-ro" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@demo-db:5432/app{{ end }}`; !strings.Contains(n.lastHCL, want) {
		t.Fatalf("HCL missing rendered credential env:\n%s", n.lastHCL)
	}
	// Project-plane jobs must use a dynamic host port (no static collision on the
	// shared node) and the peer URL must carry the port resolved from placement.
	if strings.Contains(n.lastHCL, "static =") {
		t.Fatalf("project MCP job must not pin a static host port:\n%s", n.lastHCL)
	}
	if want := "http://10.0.0.5:9100"; !strings.Contains(saved.GatewayURL, want) {
		t.Fatalf("gateway URL %q must use the resolved host port (%s)", saved.GatewayURL, want)
	}

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("duplicate: want ErrConflict, got %v", err)
	}
}

// Secret-typed param values must never land on the persisted row — only the
// non-secret params and the placeholder-bearing spec do.
func TestDeployServer_SecretParamsNeverPersisted(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	svc, _ := newService(t, ex, &fakeNomad{}, &fakeGateway{})

	saved, err := svc.DeployServer(context.Background(), "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput())
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, ok := saved.Params["bootstrap_password"]; ok {
		t.Fatalf("secret param persisted: %+v", saved.Params)
	}
	if saved.Params["db_host"] != "demo-db" {
		t.Fatalf("non-secret params must persist: %+v", saved.Params)
	}
	blob, _ := json.Marshal(saved)
	if strings.Contains(string(blob), "s3cr3t-value-9") {
		t.Fatalf("secret value leaked into the row JSON: %s", blob)
	}
	// The executor still receives the full param set (it seeds Vault with them).
	if ex.gotParams["bootstrap_password"] != "s3cr3t-value-9" {
		t.Fatalf("executor must receive secret values: %+v", ex.gotParams)
	}
}

// source=none deploys with no Vault work at all: no Instantiate, no vault block
// in the job HCL, nothing to deprovision later.
func TestDeployServer_NoneSourceSkipsVaultAndVaultBlock(t *testing.T) {
	ex := &fakeExecutor{}
	n := &fakeNomad{}
	svc, st := newService(t, ex, n, &fakeGateway{})
	ctx := context.Background()

	in := DeployInput{Name: "plain-mcp", Image: "img:1", Transport: "sse", Port: 9000,
		Credential: blueprint.CredentialSpec{Source: blueprint.SourceNone}}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(ex.instantiated) != 0 {
		t.Fatalf("none source must not Instantiate: %v", ex.instantiated)
	}
	if strings.Contains(n.lastHCL, "vault {") {
		t.Fatalf("none source must not emit a vault block:\n%s", n.lastHCL)
	}
	// Delete must succeed without a Deprovision call (empty instance).
	if err := svc.DeleteServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "plain-mcp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "plain-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row not dropped: %v", err)
	}
	// Grants without a credential source are meaningless.
	in.Name = "plain-mcp2"
	in.ExtraGrants = []blueprint.PathGrant{{Path: "pki/x", Capabilities: []string{"read"}}}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("grants with none: want ErrBadRequest, got %v", err)
	}
}

// Two dynamic deploys claiming the same engine mount would tear each other's
// engine down at deprovision — rejected up front.
func TestDeployServer_MountCollisionRejected(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	svc, _ := newService(t, ex, &fakeNomad{}, &fakeGateway{})
	ctx := context.Background()

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	second := pgInput()
	second.Name = "postgres-mcp-two"
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", second); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("mount collision: want ErrConflict, got %v", err)
	}
	// A different mount is fine.
	second.Credential.Dynamic.Mount = "database/postgres-mcp-two"
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", second); err != nil {
		t.Fatalf("distinct mount must deploy: %v", err)
	}
}

func TestDeployServer_InvalidInput(t *testing.T) {
	svc, _ := newService(t, &fakeExecutor{}, &fakeNomad{}, &fakeGateway{})
	ctx := context.Background()
	groups := []string{"project-acme-developers"}

	cases := []struct {
		name   string
		mutate func(*DeployInput)
	}{
		{"bad name", func(in *DeployInput) { in.Name = "Bad_Name!" }},
		{"missing image", func(in *DeployInput) { in.Image = "" }},
		{"bad transport", func(in *DeployInput) { in.Transport = "stdio" }},
		{"bad port", func(in *DeployInput) { in.Port = 0 }},
		{"bad credential", func(in *DeployInput) { in.Credential.Source = "magic" }},
		// (a missing required param is rejected inside Executor.Instantiate —
		// pinned by blueprint's TestInstantiate_RejectsEmptyRequiredParam)
	}
	for _, tc := range cases {
		in := pgInput()
		tc.mutate(&in)
		if _, err := svc.DeployServer(ctx, "a@x", groups, "project-acme", in); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("%s: want ErrBadRequest, got %v", tc.name, err)
		}
	}
}

func TestDeployServerRollsBackOnRegisterFailure(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{regErr: errors.New("nomad down")}
	svc, st := newService(t, ex, n, &fakeGateway{})
	ctx := context.Background()

	_, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput())
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
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{ipErr: errors.New("no placement")}
	svc, st := newService(t, ex, n, &fakeGateway{})
	ctx := context.Background()

	_, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput())
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

func TestList(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	svc, _ := newService(t, ex, &fakeNomad{existsResp: true}, &fakeGateway{})
	ctx := context.Background()

	out, err := svc.List(ctx, []string{"project-acme-developers"}, "project-acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out.Deployed) != 0 {
		t.Fatalf("deployed should start empty: %+v", out.Deployed)
	}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	out, err = svc.List(ctx, []string{"project-acme-developers"}, "project-acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out.Deployed) != 1 || !out.Deployed[0].Running || out.Deployed[0].Image == "" {
		t.Fatalf("deployed view: %+v", out.Deployed)
	}
}

func TestDeleteServer(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1"}}
	n := &fakeNomad{}
	svc, st := newService(t, ex, n, g)
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
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
	// The wire plane's VS + client tokens must go with the server, or a stale
	// wiring keeps authenticating against an empty tool set.
	found := false
	for _, n := range g.deletedVSNames {
		if n == "mcp-project-acme-postgres-mcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("wire-plane virtual server not deleted: %+v", g.deletedVSNames)
	}
	found = false
	for _, p := range g.revoked {
		if p == "mcp-project-acme-postgres-mcp-client" {
			found = true
		}
	}
	if !found {
		t.Fatalf("wire-plane client tokens not revoked: %+v", g.revoked)
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("row not dropped: %v", err)
	}
	if err := svc.DeleteServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown: want ErrNotFound, got %v", err)
	}
}

// A row persisted before Phase F (catalog deploy: blueprint_ref value + instance
// blob) must still deprovision through the same delete path.
func TestDeleteServer_LegacyRowStillDeprovisions(t *testing.T) {
	ex := &fakeExecutor{}
	n := &fakeNomad{}
	svc, st := newService(t, ex, n, &fakeGateway{})
	ctx := context.Background()

	instBlob, _ := json.Marshal(instanceRecord())
	if _, err := st.UpsertProjectMCPServer(ctx, store.ProjectMCPServer{
		Project: "project-acme", Name: "legacy-mcp", Status: store.StatusDeployed,
		BlueprintRef: &store.BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: "abc"},
		Instance:     instBlob, JobID: "job-legacy", PeerID: "peer-legacy", Transport: "sse",
	}); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := svc.DeleteServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "legacy-mcp"); err != nil {
		t.Fatalf("delete legacy: %v", err)
	}
	if len(ex.deprovisoned) != 1 || ex.deprovisoned[0].WIFRoleName != "mcp-postgres-mcp" {
		t.Fatalf("legacy instance must deprovision: %+v", ex.deprovisoned)
	}
	if len(n.purged) != 1 || n.purged[0] != "job-legacy" {
		t.Fatalf("legacy job must purge: %+v", n.purged)
	}
}

func passingProbe() mcpgw.ScopeProbe {
	return mcpgw.ScopeProbe{OwnServerOK: true, AdminDenied: true, OtherServerDenied: true, OtherServerChecked: true}
}

func TestTestServer(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1", "t2"}}
	svc, _ := newService(t, ex, &fakeNomad{}, g)
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
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

// TestServerTools pins the tool-catalog surface for template authoring: Test
// caches the gateway's tools (with names) on the row, and ServerTools returns
// that cache without a live gateway call.
func TestServerTools(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	g := &fakeGateway{
		probe:     passingProbe(),
		tools:     []string{"t1", "t2"},
		toolInfos: []mcpgw.ToolInfo{{ID: "t1", Name: "search", Description: "full-text"}, {ID: "t2", Name: "fetch"}},
	}
	svc, _ := newService(t, ex, &fakeNomad{}, g)
	ctx := context.Background()
	groups := []string{"project-acme-developers"}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", groups, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if _, err := svc.TestServer(ctx, "acme-admin@x", groups, "project-acme", "postgres-mcp"); err != nil {
		t.Fatalf("test: %v", err)
	}

	tools, err := svc.ServerTools(ctx, groups, "project-acme", "postgres-mcp")
	if err != nil {
		t.Fatalf("ServerTools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "search" || tools[0].Description != "full-text" {
		t.Fatalf("cached tool catalog not returned: %+v", tools)
	}
	if _, err := svc.ServerTools(ctx, groups, "project-acme", "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown server: want ErrNotFound, got %v", err)
	}
}

// TestTestServer_HealsStalePeerURL pins Test's reconcile contract: a Nomad
// reschedule (or a heal that resolved the dying allocation — observed live) leaves
// row.GatewayURL and the gateway peer pointing at a dead host port, and
// ContextForge deactivates the peer after 3 failed health checks. Test must
// re-resolve the live placement, rewrite the peer URL in place, re-activate it,
// and persist the fresh URL — the button that reports the breakage repairs it.
func TestTestServer_HealsStalePeerURL(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	g := &fakeGateway{probe: passingProbe(), tools: []string{"t1"}}
	svc, st := newService(t, ex, &fakeNomad{}, g)
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	// Simulate the stale state: the row (and peer) carry a dead allocation's port.
	row, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	row.GatewayURL = "http://10.0.0.5:31999/mcp"
	row.PeerID = "peer-stale"
	if _, err := st.UpsertProjectMCPServer(ctx, row); err != nil {
		t.Fatalf("seed stale row: %v", err)
	}

	got, err := svc.TestServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp")
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got.GatewayURL != "http://10.0.0.5:9100/mcp" {
		t.Fatalf("gateway URL must be re-resolved from the live allocation: %q", got.GatewayURL)
	}
	if len(g.updatedPeers) != 1 || !strings.Contains(g.updatedPeers[0], "http://10.0.0.5:9100/mcp") {
		t.Fatalf("peer must be updated in place at the live address: %v", g.updatedPeers)
	}
	if len(g.activatedPeers) != 1 {
		t.Fatalf("peer must be re-activated: %v", g.activatedPeers)
	}
	if len(g.deletedPeers) != 0 {
		t.Fatalf("heal must not delete the peer: %v", g.deletedPeers)
	}
}

// A redeploy of a server some template already references must re-run the
// workspace wiring automatically — forgetting Apply add-ons after a
// delete+redeploy left workspaces on the previous (dead) virtual server.
func TestDeployServer_RewiresWhenTemplateReferences(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	st := store.NewMemory()
	w := &fakeWirer{}
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, &fakeNomad{}, &fakeGateway{}, w, Config{})
	ctx := context.Background()

	if _, err := st.UpsertProjectTemplate(ctx, store.ProjectTemplate{
		Project: "project-acme", Flavor: "dev-workspace", Status: "ready",
		Addons: store.TemplateAddons{MCPServers: []string{"postgres-mcp"}},
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(w.wired) != 1 || w.wired[0] != "project-acme/postgres-mcp" {
		t.Fatalf("referenced server not re-wired: %+v", w.wired)
	}

	// A wire failure must not undo the deploy, but must surface loudly.
	w2 := &fakeWirer{err: errors.New("gateway down")}
	svc2 := New(st, fakeProjects{ns: "project-acme"}, &fakeExecutor{rec: instanceRecord()}, &fakeNomad{}, &fakeGateway{}, w2, Config{})
	in := pgInput()
	in.Name = "postgres-mcp2"
	in.Credential.Dynamic.Mount = "database/postgres-mcp2"
	if _, err := st.UpsertProjectTemplate(ctx, store.ProjectTemplate{
		Project: "project-acme", Flavor: "gpu-workspace", Status: "ready",
		Addons: store.TemplateAddons{MCPServers: []string{"postgres-mcp2"}},
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	if _, err := svc2.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err == nil {
		t.Fatalf("wire failure must surface as an error")
	} else if !errors.Is(err, apperr.ErrConflict) {
		// Typed so the api layer forwards the remediation message to the UI
		// instead of flattening it to a generic upstream error.
		t.Fatalf("wire failure must be ErrConflict, got: %v", err)
	}
	if _, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp2"); err != nil {
		t.Fatalf("deploy must survive a wire failure: %v", err)
	}
}

func TestDeployServer_NoRewireWhenUnreferenced(t *testing.T) {
	w := &fakeWirer{}
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, &fakeExecutor{rec: instanceRecord()}, &fakeNomad{}, &fakeGateway{}, w, Config{})
	if _, err := svc.DeployServer(context.Background(), "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(w.wired) != 0 {
		t.Fatalf("unreferenced server must not wire: %+v", w.wired)
	}
}

func TestDeployServer_ThreadsExtraGrants(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	svc, _ := newService(t, ex, &fakeNomad{}, &fakeGateway{})

	in := pgInput()
	in.ExtraGrants = []blueprint.PathGrant{{Path: "pki/project-acme/issue/web", Capabilities: []string{"create", "update"}}}
	if _, err := svc.DeployServer(context.Background(), "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(ex.gotGrants) != 1 || ex.gotGrants[0].Path != "pki/project-acme/issue/web" {
		t.Fatalf("ExtraGrants not threaded to executor: %+v", ex.gotGrants)
	}
}

// UpdateServer with an UNCHANGED container definition is a pure control-plane
// policy rewrite: the
// executor gets the persisted record + spec with the replacement grants, the row's
// instance blob is updated in place, and NOTHING else moves — no nomad, no
// gateway, no wirer.
func TestUpdateServer_PolicyOnlyNoChurn(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{}
	svc, _ := newService(t, ex, n, g)
	ctx := context.Background()

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	regAfterDeploy := n.regCount
	registeredBefore := len(g.registered)

	grants := []blueprint.PathGrant{{Path: "secret/data/projects/team-config", Capabilities: []string{"read"}}}
	saved, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp", updFrom(pgInput(), nil, grants))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(ex.updated) != 1 || ex.updated[0] != "postgres-mcp" {
		t.Fatalf("executor UpdateGrants calls: %v", ex.updated)
	}
	if ex.gotSpec.Source != blueprint.SourceDynamic {
		t.Fatalf("persisted spec not threaded to executor: %+v", ex.gotSpec)
	}
	var rec blueprint.InstanceRecord
	if err := json.Unmarshal(saved.Instance, &rec); err != nil || len(rec.ExtraGrants) != 1 || rec.ExtraGrants[0].Path != "secret/data/projects/team-config" {
		t.Fatalf("row instance must carry the new grants: %v %+v", err, rec)
	}
	if n.regCount != regAfterDeploy || len(n.purged) != 0 || len(g.registered) != registeredBefore || len(g.deletedPeers) != 0 || len(g.revoked) != 0 {
		t.Fatalf("policy-only update must not touch nomad or the gateway: reg=%d purged=%v registered=%v deletedPeers=%v revoked=%v",
			n.regCount, n.purged, g.registered, g.deletedPeers, g.revoked)
	}

	if _, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "nope", updFrom(pgInput(), nil, grants)); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown server: want ErrNotFound, got %v", err)
	}

	ex.updateErr = apperr.ErrForbidden
	if _, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp", updFrom(pgInput(), nil, grants)); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("executor error must propagate typed, got %v", err)
	}
}

// An env change resubmits the SAME job name re-rendered from the persisted row
// (image/command/port/credential env templates — with non-secret params only),
// records the new placement URL, and heals the gateway peer IN PLACE (UpdatePeer
// at the new address): the virtual server and its scoped token survive, so NO
// re-wire runs and existing workspaces keep working. The Vault instance is NOT
// re-provisioned.
func TestUpdateServer_EnvChangeResubmitsJobAndHealsPeer(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{}
	w := &fakeWirer{}
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, w, Config{})
	ctx := context.Background()

	if _, err := st.UpsertProjectTemplate(ctx, store.ProjectTemplate{
		Project: "project-acme", Flavor: "dev-workspace", Status: "ready",
		Addons: store.TemplateAddons{MCPServers: []string{"postgres-mcp"}},
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	in := pgInput()
	in.Env = map[string]string{"LOG_LEVEL": "info"}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	// Simulate a prior Test having registered a peer for this server.
	row, err := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	row.PeerID = "peer-old"
	if _, err := st.UpsertProjectMCPServer(ctx, row); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	wiredAfterDeploy := len(w.wired)

	saved, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp",
		updFrom(in, map[string]string{"LOG_LEVEL": "debug"}, nil))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if saved.Env["LOG_LEVEL"] != "debug" {
		t.Fatalf("row env not updated: %+v", saved.Env)
	}
	if !strings.Contains(n.lastHCL, `LOG_LEVEL = "debug"`) || !strings.Contains(n.lastHCL, `job "mcp-project-acme-postgres-mcp"`) {
		t.Fatalf("job not re-rendered with the new env:\n%s", n.lastHCL)
	}
	// Credential env re-rendered from the persisted NON-secret params.
	if !strings.Contains(n.lastHCL, "demo-db:5432/app") {
		t.Fatalf("credential env template lost on re-render:\n%s", n.lastHCL)
	}
	if len(ex.instantiated) != 1 {
		t.Fatalf("env edit must NOT re-instantiate the Vault credential: %v", ex.instantiated)
	}
	if len(g.updatedPeers) != 1 || g.updatedPeers[0] != "peer-old http://10.0.0.5:9100/mcp streamable-http" {
		t.Fatalf("peer must be updated in place at the new address: %v", g.updatedPeers)
	}
	if len(g.deletedPeers) != 0 || saved.PeerID != "peer-old" {
		t.Fatalf("healed peer must be kept: deleted=%v peerID=%q", g.deletedPeers, saved.PeerID)
	}
	if saved.GatewayURL != "http://10.0.0.5:9100/mcp" {
		t.Fatalf("gateway URL must track the new placement: %q", saved.GatewayURL)
	}
	if len(g.activatedPeers) != 1 || g.activatedPeers[0] != "peer-old" {
		t.Fatalf("healed peer must be re-activated (ContextForge may have deactivated it during the restart window): %v", g.activatedPeers)
	}
	if len(w.wired) != wiredAfterDeploy || len(g.createdVS) != 0 || len(g.revoked) != 0 {
		t.Fatalf("a healed peer must NOT re-wire (VS + token survive): wired=%v vs=%v revoked=%v", w.wired, g.createdVS, g.revoked)
	}
	if len(n.purged) != 0 {
		t.Fatalf("an env edit must never purge the job: %v", n.purged)
	}
}

// If the row has no peer id (wired but never tested), the env path finds—or
// creates at the new URL—the canonical-name peer, then updates it in place.
func TestUpdateServer_EnvChangeWithoutPeerIDFindsByName(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{}
	w := &fakeWirer{}
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, w, Config{})
	ctx := context.Background()

	in := pgInput()
	in.Env = map[string]string{"LOG_LEVEL": "info"}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	saved, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp",
		updFrom(in, map[string]string{"LOG_LEVEL": "debug"}, nil))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(g.registered) != 1 || g.registered[0] != "mcp-project-acme-postgres-mcp" {
		t.Fatalf("peer must be found/created by canonical name: %v", g.registered)
	}
	if len(g.updatedPeers) != 1 || saved.PeerID != "peer-mcp-project-acme-postgres-mcp" {
		t.Fatalf("peer must be healed and recorded: updated=%v peerID=%q", g.updatedPeers, saved.PeerID)
	}
	if len(w.wired) != 0 {
		t.Fatalf("healed peer must not wire: %v", w.wired)
	}
}

// If the in-place heal fails, fall back to the destructive-but-proven path: drop
// the peer and re-wire referencing templates (fresh VS + client token).
func TestUpdateServer_PeerHealFailureFallsBackToRewire(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{updatePeerErr: errors.New("PUT /gateways: status 500")}
	w := &fakeWirer{}
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, w, Config{})
	ctx := context.Background()

	if _, err := st.UpsertProjectTemplate(ctx, store.ProjectTemplate{
		Project: "project-acme", Flavor: "dev-workspace", Status: "ready",
		Addons: store.TemplateAddons{MCPServers: []string{"postgres-mcp"}},
	}); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	in := pgInput()
	in.Env = map[string]string{"LOG_LEVEL": "info"}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	row, _ := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	row.PeerID = "peer-old"
	if _, err := st.UpsertProjectMCPServer(ctx, row); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	wiredAfterDeploy := len(w.wired)

	saved, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp",
		updFrom(in, map[string]string{"LOG_LEVEL": "debug"}, nil))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(g.deletedPeers) != 1 || g.deletedPeers[0] != "peer-old" || saved.PeerID != "" {
		t.Fatalf("failed heal must drop the stale peer: deleted=%v peerID=%q", g.deletedPeers, saved.PeerID)
	}
	if len(w.wired) != wiredAfterDeploy+1 {
		t.Fatalf("failed heal must fall back to re-wiring: %v", w.wired)
	}
}

// A placement failure after the job update must not purge the job and must leave
// the persisted row untouched (old env, old URL), surfacing as ErrConflict with
// the remediation in the message.
func TestUpdateServer_PlacementFailureKeepsRow(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{}
	svc, st := newService(t, ex, n, g)
	ctx := context.Background()

	in := pgInput()
	in.Env = map[string]string{"LOG_LEVEL": "info"}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	before, _ := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")

	n.ipErr = errors.New("no allocation placed")
	_, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp",
		updFrom(in, map[string]string{"LOG_LEVEL": "debug"}, nil))
	if !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("placement failure: want ErrConflict, got %v", err)
	}
	after, _ := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	if after.Env["LOG_LEVEL"] != before.Env["LOG_LEVEL"] || after.GatewayURL != before.GatewayURL {
		t.Fatalf("row must be untouched on placement failure: before=%+v after=%+v", before, after)
	}
	if len(n.purged) != 0 {
		t.Fatalf("must not purge a previously-working server: %v", n.purged)
	}
}

// Grants without a credential source have no policy to land on (mirrors the
// deploy-time rule).
func TestUpdateServer_GrantsWithoutCredentialIsBadRequest(t *testing.T) {
	svc, _ := newService(t, &fakeExecutor{}, &fakeNomad{}, &fakeGateway{})
	ctx := context.Background()

	in := DeployInput{Name: "plain-mcp", Image: "ghcr.io/x/plain:1", Transport: "sse", Port: 8080,
		Credential: blueprint.CredentialSpec{Source: blueprint.SourceNone}}
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", in); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	_, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "plain-mcp",
		updFrom(in, nil, []blueprint.PathGrant{{Path: "secret/data/projects/x", Capabilities: []string{"read"}}}))
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("grants on source=none: want ErrBadRequest, got %v", err)
	}
}

// A definition change (image, transport, path — not just env) rides the same
// resubmit-and-heal path: the job is re-rendered with the NEW definition, the
// gateway URL tracks the new transport's default path, and the peer is updated
// in place with the new transport — no re-wire, no re-instantiate, no purge.
func TestUpdateServer_DefinitionChangeResubmitsAndHealsPeer(t *testing.T) {
	ex := &fakeExecutor{rec: instanceRecord()}
	n := &fakeNomad{}
	g := &fakeGateway{}
	w := &fakeWirer{}
	st := store.NewMemory()
	svc := New(st, fakeProjects{ns: "project-acme"}, ex, n, g, w, Config{})
	ctx := context.Background()

	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	row, _ := st.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	row.PeerID = "peer-old"
	if _, err := st.UpsertProjectMCPServer(ctx, row); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	upd := updFrom(pgInput(), nil, nil)
	upd.Image = "ghcr.io/x/postgres-mcp:2"
	upd.Transport = "sse"
	upd.Command = []string{"/bin/server", "--sse"}
	saved, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp", upd)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if saved.Image != "ghcr.io/x/postgres-mcp:2" || saved.Transport != "sse" || len(saved.Command) != 2 {
		t.Fatalf("row must carry the new definition: %+v", saved)
	}
	if !strings.Contains(n.lastHCL, "ghcr.io/x/postgres-mcp:2") || !strings.Contains(n.lastHCL, "--sse") {
		t.Fatalf("job not re-rendered with the new definition:\n%s", n.lastHCL)
	}
	if saved.GatewayURL != "http://10.0.0.5:9100/sse" {
		t.Fatalf("gateway URL must track the new transport's default path: %q", saved.GatewayURL)
	}
	if len(g.updatedPeers) != 1 || g.updatedPeers[0] != "peer-old http://10.0.0.5:9100/sse sse" {
		t.Fatalf("peer must be healed with the new URL + transport: %v", g.updatedPeers)
	}
	if len(ex.instantiated) != 1 || len(n.purged) != 0 || len(w.wired) != 0 {
		t.Fatalf("definition edit must not re-instantiate/purge/re-wire: inst=%v purged=%v wired=%v",
			ex.instantiated, n.purged, w.wired)
	}
}

// The update input is the FULL replacement definition, validated like a deploy.
func TestUpdateServer_RejectsInvalidDefinition(t *testing.T) {
	svc, _ := newService(t, &fakeExecutor{rec: instanceRecord()}, &fakeNomad{}, &fakeGateway{})
	ctx := context.Background()
	if _, err := svc.DeployServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", pgInput()); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*UpdateInput)
	}{
		{"empty-image", func(u *UpdateInput) { u.Image = "" }},
		{"bad-transport", func(u *UpdateInput) { u.Transport = "stdio" }},
		{"bad-port", func(u *UpdateInput) { u.Port = 0 }},
	} {
		upd := updFrom(pgInput(), nil, nil)
		tc.mutate(&upd)
		if _, err := svc.UpdateServer(ctx, "acme-admin@x", []string{"project-acme-developers"}, "project-acme", "postgres-mcp", upd); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("%s: want ErrBadRequest, got %v", tc.name, err)
		}
	}
}
