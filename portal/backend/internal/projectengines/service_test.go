package projectengines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// fakeVault embeds blueprint.VaultAdmin (nil): only the methods Provision calls are
// overridden, so the many unused interface methods need no stubs.
type fakeVault struct {
	blueprint.VaultAdmin
	ops       []string
	mountErr  error // returned by every MountEngine (e.g. "already in use")
	failOnKVv string
}

func (f *fakeVault) MountEngine(_ context.Context, ns, p, t, v string) error {
	f.ops = append(f.ops, "mount:"+p)
	return f.mountErr
}
func (f *fakeVault) WriteSSHCA(_ context.Context, ns, m string) error {
	f.ops = append(f.ops, "ssh-ca")
	return nil
}
func (f *fakeVault) WriteSSHRole(_ context.Context, ns, m, n string, r blueprint.SSHRole) error {
	f.ops = append(f.ops, "ssh-role:"+r.DefaultUser)
	return nil
}
func (f *fakeVault) WriteGitHubConfig(_ context.Context, ns, m string, id int, pem string) error {
	f.ops = append(f.ops, "gh-config:"+pem) // proves the PEM reaches Vault (and only here)
	return nil
}
func (f *fakeVault) WriteGitHubPermissionSet(_ context.Context, ns, m, n string, id int, perms map[string]string, repos []string) error {
	f.ops = append(f.ops, "gh-permset")
	return nil
}
func (f *fakeVault) WritePolicy(_ context.Context, ns, n, h string) error {
	f.ops = append(f.ops, "policy:"+n)
	return nil
}
func (f *fakeVault) WriteKVv2(_ context.Context, ns, m, p string, d map[string]any) error {
	f.ops = append(f.ops, "kv:"+p)
	if f.failOnKVv == p {
		return errors.New("kv boom")
	}
	return nil
}
func (f *fakeVault) CreatePeriodicToken(_ context.Context, ns string, pols []string, period string) (string, error) {
	f.ops = append(f.ops, "periodic-token:"+period)
	return "vault-tok", nil
}

type fakeBoundary struct{ storeAddr, storeToken, libPath, libKeyID string }

func (b *fakeBoundary) CreateVaultCredentialStore(_ context.Context, scopeID, name, addr, ns, token string) (string, error) {
	b.storeAddr, b.storeToken = addr, token
	return "cs_1", nil
}
func (b *fakeBoundary) CreateSSHCertLibrary(_ context.Context, storeID, name, path, user, keyID string) (string, error) {
	b.libPath, b.libKeyID = path, keyID
	return "clvlt_1", nil
}

type fakeLLM struct{ spec llmgw.KeySpec }

func (l *fakeLLM) GenerateKey(_ context.Context, spec llmgw.KeySpec) (string, error) {
	l.spec = spec
	return "sk-123", nil
}

func (l *fakeLLM) DeleteKeyByAlias(_ context.Context, _ string) error { return nil }

type fakeProjects struct{ d descriptor.Descriptor }

func (f fakeProjects) GetProject(_ context.Context, name string, _ []string) (descriptor.Descriptor, error) {
	if name != f.d.ProjectName {
		return descriptor.Descriptor{}, apperr.ErrNotFound
	}
	return f.d, nil
}

func setup(t *testing.T) (*store.Memory, *fakeVault, *fakeBoundary, *Service) {
	t.Helper()
	st := store.NewMemory()
	d := descriptor.Descriptor{ProjectName: "project-beta", Namespace: "project-beta", ProjectScopeID: "p_1", WorkspaceUser: "dev"}
	js, _ := json.Marshal(d)
	if _, err := st.UpsertProjectDescriptor(context.Background(), store.ProjectDescriptor{Project: "project-beta", Status: store.StatusReady, Descriptor: js}); err != nil {
		t.Fatal(err)
	}
	fv, fb, fl := &fakeVault{}, &fakeBoundary{}, &fakeLLM{}
	svc := New(fv, fb, fl, nil, st, fakeProjects{d: d}, st, Config{
		VaultCredStoreAddress: "https://vault:8200", LLMGatewayPrivateEndpoint: "http://10.0.0.1:4000",
	})
	return st, fv, fb, svc
}

func TestProvision_HappyPath(t *testing.T) {
	st, fv, fb, svc := setup(t)
	ctx := context.Background()

	out, err := svc.Provision(ctx, "admin@x", nil, "project-beta", ProvisionInput{
		GithubAppID: 42, GithubAppInstallationID: 99, GithubAppPrivateKey: "PEM-DATA",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if out.CredentialLibraryID != "clvlt_1" {
		t.Fatalf("descriptor credential_library_id = %q, want clvlt_1", out.CredentialLibraryID)
	}
	if !out.GithubConfigured {
		t.Fatalf("github_configured should be true when creds are supplied")
	}
	// Descriptor row persisted with the library id + ready status.
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	if pd.Status != store.StatusReady {
		t.Fatalf("descriptor status = %q, want ready", pd.Status)
	}
	dd, _ := descriptor.Parse(string(pd.Descriptor))
	if dd.CredentialLibraryID != "clvlt_1" {
		t.Fatalf("persisted credential_library_id = %q", dd.CredentialLibraryID)
	}
	// Boundary got the brokered periodic token + the ssh sign path + verbatim key_id.
	if fb.storeToken != "vault-tok" || fb.libPath != "ssh/sign/dev-workspace" || fb.libKeyID != "{{.User.Email}}" {
		t.Fatalf("boundary wiring wrong: token=%q path=%q keyid=%q", fb.storeToken, fb.libPath, fb.libKeyID)
	}
	// The PEM reached Vault exactly once (github config) and nowhere else.
	pemOps := 0
	for _, op := range fv.ops {
		if strings.Contains(op, "PEM-DATA") {
			pemOps++
		}
	}
	if pemOps != 1 {
		t.Fatalf("PEM appeared in %d ops, want exactly 1 (github config)", pemOps)
	}
	// 5 WIF read policies + the boundary-cred-store policy were written.
	if !slices.Contains(fv.ops, "policy:nomad-project-beta-ca-read") || !slices.Contains(fv.ops, "policy:boundary-cred-store") {
		t.Fatalf("policies not written: %v", fv.ops)
	}
}

// Provision with EMPTY GitHub creds now SUCCEEDS: the github mount is created but the
// App config is deferred (a project-admin sets it later). This is the project-create
// path (ProvisionAtCreate delegates here with an empty input).
func TestProvision_EmptyGithubCredsProvisionsMountOnly(t *testing.T) {
	st, fv, _, svc := setup(t)
	ctx := context.Background()
	out, err := svc.Provision(ctx, "admin@x", nil, "project-beta", ProvisionInput{})
	if err != nil {
		t.Fatalf("Provision with empty creds should succeed, got %v", err)
	}
	if out.CredentialLibraryID != "clvlt_1" {
		t.Fatalf("engines should still stand up: %+v", out)
	}
	if out.GithubConfigured {
		t.Fatalf("github should NOT be configured without creds")
	}
	// github mount created, but no gh-config/gh-permset ops.
	if !slices.Contains(fv.ops, "mount:github") {
		t.Fatalf("github mount not created: %v", fv.ops)
	}
	for _, op := range fv.ops {
		if strings.HasPrefix(op, "gh-config") || op == "gh-permset" {
			t.Fatalf("github config written despite no creds: %v", fv.ops)
		}
	}
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	dd, _ := descriptor.Parse(string(pd.Descriptor))
	if dd.GithubConfigured {
		t.Fatalf("descriptor github_configured should be false")
	}
}

// SetGitHubCredentials rejects an incomplete set and, given a full set, writes the
// config + permission set and flips the descriptor's github_configured flag.
func TestSetGitHubCredentials(t *testing.T) {
	st, fv, _, svc := setup(t)
	ctx := context.Background()

	if err := svc.SetGitHubCredentials(ctx, "admin@x", nil, "project-beta", ProvisionInput{GithubAppID: 42}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("incomplete creds: err = %v, want ErrBadRequest", err)
	}

	if err := svc.SetGitHubCredentials(ctx, "admin@x", nil, "project-beta", ProvisionInput{
		GithubAppID: 42, GithubAppInstallationID: 99, GithubAppPrivateKey: "PEM-DATA",
		GithubRepositories: []string{"org/repo-a"},
	}); err != nil {
		t.Fatalf("SetGitHubCredentials: %v", err)
	}
	if !slices.Contains(fv.ops, "gh-config:PEM-DATA") || !slices.Contains(fv.ops, "gh-permset") {
		t.Fatalf("github config/permset not written: %v", fv.ops)
	}
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	dd, _ := descriptor.Parse(string(pd.Descriptor))
	if !dd.GithubConfigured {
		t.Fatalf("descriptor github_configured not set")
	}

	// Status reflects it.
	stt, err := svc.Status(ctx, nil, "project-beta")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !stt.GithubConfigured {
		t.Fatalf("status github_configured = false, want true")
	}
	// Non-secret coordinates are echoed back for form prefill; never the key.
	// Repositories are normalized to bare names (URL/owner-prefixed forms would
	// 422 at token mint).
	if stt.GithubAppID != 42 || stt.GithubAppInstallationID != 99 || !slices.Contains(stt.GithubRepositories, "repo-a") {
		t.Fatalf("status github coords not persisted/normalized: %+v", stt)
	}
}

// normalizeRepos accepts what admins naturally paste and reduces each entry to
// the bare repository name the GitHub App API expects.
func TestNormalizeRepos(t *testing.T) {
	got := normalizeRepos([]string{
		"https://github.com/panchal-ravi/confused-deputy-aws.git",
		"git@github.com:owner/ssh-repo.git",
		"owner/plain-repo",
		"bare-repo",
		"  spaced-repo  ",
		"",
		"https://github.com/owner/trailing/",
	})
	want := []string{"confused-deputy-aws", "ssh-repo", "plain-repo", "bare-repo", "spaced-repo", "trailing"}
	if len(got) != len(want) {
		t.Fatalf("normalizeRepos = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeRepos[%d] = %q, want %q (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestProvision_MountAlreadyInUseIsIdempotent(t *testing.T) {
	_, fv, _, svc := setup(t)
	fv.mountErr = errors.New("path is already in use at ssh/")
	_, err := svc.Provision(context.Background(), "admin@x", nil, "project-beta", ProvisionInput{
		GithubAppID: 42, GithubAppInstallationID: 99, GithubAppPrivateKey: "PEM",
	})
	if err != nil {
		t.Fatalf("re-mount 'already in use' must be swallowed, got %v", err)
	}
}

func TestProvision_FailureMarksDescriptorError(t *testing.T) {
	st, fv, _, svc := setup(t)
	fv.failOnKVv = "projects/llm" // fail at the LLM KV write
	_, err := svc.Provision(context.Background(), "admin@x", nil, "project-beta", ProvisionInput{
		GithubAppID: 42, GithubAppInstallationID: 99, GithubAppPrivateKey: "PEM",
	})
	if err == nil || !strings.Contains(err.Error(), "llm.kv") {
		t.Fatalf("err = %v, want a wrapped llm.kv failure", err)
	}
	pd, _ := st.GetProjectDescriptor(context.Background(), "project-beta")
	if pd.Status != store.StatusError {
		t.Fatalf("descriptor status = %q, want error", pd.Status)
	}
}

// fakeGateway embeds mcpgw.Client (nil): only the methods WireMCPServer calls are
// overridden. It mirrors ContextForge's decisive token semantics (verified live):
// a token name is reserved FOREVER once used — creating a duplicate fails even
// after the original was revoked.
type fakeGateway struct {
	mcpgw.Client
	peerName, vsName string
	tokenNames       []string        // every CreateScopedToken name, in order
	usedNames        map[string]bool // reserved forever, revoke does not free
	revoked          []string        // RevokeTokensByPrefix prefixes, in order
}

func (g *fakeGateway) RegisterPeer(_ context.Context, name, url, transport string) (string, error) {
	g.peerName = name
	return "peer-1", nil
}
func (g *fakeGateway) DiscoverTools(_ context.Context, peerID string) ([]string, error) {
	return []string{"t1"}, nil
}
func (g *fakeGateway) CreateVirtualServer(_ context.Context, name, desc string, toolIDs []string) (string, error) {
	g.vsName = name
	return "vs-1", nil
}
func (g *fakeGateway) CreateScopedToken(_ context.Context, name string, days int, serverID string) (string, error) {
	if g.usedNames[name] {
		return "", fmt.Errorf("mcpgw: POST /tokens: status 400: token name %q already exists", name)
	}
	if g.usedNames == nil {
		g.usedNames = map[string]bool{}
	}
	g.usedNames[name] = true
	g.tokenNames = append(g.tokenNames, name)
	return "tok-" + name, nil
}
func (g *fakeGateway) RevokeTokensByPrefix(_ context.Context, prefix string) error {
	g.revoked = append(g.revoked, prefix)
	return nil
}

// TestWireMCPServer_PeerNameMatchesDeployPlane pins the cross-plane contract behind
// the add-ons 409 "Gateway already exists": the deploy plane (projectadmin Test)
// registers the peer as mcp-<project>-<server>, and ContextForge dedupes gateways by
// URL, so WireMCPServer MUST reuse the same name for RegisterPeer's by-name
// idempotency to find it.
func TestWireMCPServer_PeerNameMatchesDeployPlane(t *testing.T) {
	st, fv, _, _ := setup(t)
	d := descriptor.Descriptor{ProjectName: "project-beta", Namespace: "project-beta", ProjectScopeID: "p_1", WorkspaceUser: "dev"}
	gw := &fakeGateway{}
	svc := New(fv, &fakeBoundary{}, &fakeLLM{}, gw, st, fakeProjects{d: d}, st, Config{
		MCPGatewayEndpoint: "http://10.0.0.9:4444",
	})
	if _, err := st.UpsertProjectMCPServer(context.Background(), store.ProjectMCPServer{
		Project: "project-beta", Name: "everything", Status: "deployed",
		GatewayURL: "http://10.0.0.5:21800/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.WireMCPServer(context.Background(), "project-beta", "everything"); err != nil {
		t.Fatalf("WireMCPServer: %v", err)
	}
	if gw.peerName != "mcp-project-beta-everything" {
		t.Errorf("peer name = %q, want mcp-project-beta-everything (must match the deploy plane's service name)", gw.peerName)
	}
	if !slices.Contains(fv.ops, "kv:projects/mcp/everything") {
		t.Errorf("kv write missing: ops = %v", fv.ops)
	}
}

// TestWireMCPServer_RewireConverges pins the delete+redeploy regression: applying
// add-ons a second time must succeed even though ContextForge reserves token names
// forever (a fixed "<peer>-client" name 400s on the second wire). The wiring must
// revoke prior client tokens by prefix and mint a uniquely-named fresh one.
func TestWireMCPServer_RewireConverges(t *testing.T) {
	st, fv, _, _ := setup(t)
	d := descriptor.Descriptor{ProjectName: "project-beta", Namespace: "project-beta", ProjectScopeID: "p_1", WorkspaceUser: "dev"}
	gw := &fakeGateway{}
	svc := New(fv, &fakeBoundary{}, &fakeLLM{}, gw, st, fakeProjects{d: d}, st, Config{
		MCPGatewayEndpoint: "http://10.0.0.9:4444",
	})
	if _, err := st.UpsertProjectMCPServer(context.Background(), store.ProjectMCPServer{
		Project: "project-beta", Name: "everything", Status: "deployed",
		GatewayURL: "http://10.0.0.5:21800/mcp", Transport: "streamable-http",
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := svc.WireMCPServer(context.Background(), "project-beta", "everything"); err != nil {
			t.Fatalf("WireMCPServer (wire %d): %v", i, err)
		}
	}
	if len(gw.tokenNames) != 2 || gw.tokenNames[0] == gw.tokenNames[1] {
		t.Fatalf("token names = %v, want 2 distinct", gw.tokenNames)
	}
	for _, n := range gw.tokenNames {
		if !strings.HasPrefix(n, "mcp-project-beta-everything-client") {
			t.Errorf("token name %q lacks the mcp-project-beta-everything-client prefix (revoke-by-prefix would miss it)", n)
		}
	}
	if !slices.Contains(gw.revoked, "mcp-project-beta-everything-client") {
		t.Errorf("stale client tokens not revoked by prefix: revoked = %v", gw.revoked)
	}
}
