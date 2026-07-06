// Package projectengines is the project-facing engine-provision plane: a
// project-admin stands up the per-project secret engines (Vault SSH CA + GitHub App
// broker + the workspace WIF read policies + a LiteLLM virtual key) and the Boundary
// credential store + SSH-certificate library that make the project's workspaces
// launchable — all from the Portal, replacing terraform/project. Every step runs
// under a token native to the project's Vault namespace (the §5 broker); the GitHub
// App private key is write-only. Steps run in dependency order and are idempotent, so
// a re-run after a transient failure converges (the recovery path; no destructive
// rollback, matching projectbootstrap).
package projectengines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// ProjectLookup resolves the descriptor + enforces membership (satisfied by
// *workspace.Service). It yields the namespace, project scope, and workspace user.
type ProjectLookup interface {
	GetProject(ctx context.Context, name string, groups []string) (descriptor.Descriptor, error)
}

// BoundaryCreds is the Boundary credential-store/library surface (satisfied by
// *hashistack.Boundary).
type BoundaryCreds interface {
	CreateVaultCredentialStore(ctx context.Context, scopeID, name, vaultAddr, vaultNamespace, token string) (string, error)
	CreateSSHCertLibrary(ctx context.Context, storeID, name, path, username, keyID string) (string, error)
}

// LLMKeyGen mints the project's LiteLLM virtual key (satisfied by llmgw.Client).
type LLMKeyGen interface {
	GenerateKey(ctx context.Context, spec llmgw.KeySpec) (key string, err error)
	// DeleteKeyByAlias frees a virtual-key alias so re-provision can re-mint it
	// (LiteLLM requires unique aliases and rejects a colliding generate).
	DeleteKeyByAlias(ctx context.Context, alias string) error
}

// DescriptorStore reads + writes the project descriptor row and reads deployed MCP
// server rows (satisfied by store.Store).
type DescriptorStore interface {
	GetProjectDescriptor(ctx context.Context, project string) (store.ProjectDescriptor, error)
	UpsertProjectDescriptor(ctx context.Context, d store.ProjectDescriptor) (store.ProjectDescriptor, error)
	GetProjectMCPServer(ctx context.Context, project, name string) (store.ProjectMCPServer, error)
}

// Auditor records provision events (satisfied by store.Store).
type Auditor interface {
	AppendAudit(ctx context.Context, ev store.AuditEvent) error
}

// Config carries the infra coordinates + PoC guardrails the provision needs.
type Config struct {
	VaultCredStoreAddress     string   // Vault address Boundary authenticates to
	LLMGatewayPrivateEndpoint string   // base_url written into secret/projects/llm
	MCPGatewayEndpoint        string   // private ContextForge base URL; workspace MCP url = <this>/servers/<vs>/sse
	GithubPluginVersion       string   // e.g. "2.3.2" (mounted as v<version>)
	CertTTL                   string   // SSH cert TTL (default "5m")
	CertMaxTTL                string   // SSH cert max TTL (default "30m")
	BoundaryTokenPeriod       string   // periodic token period (default "24h")
	MCPTokenDays              int      // scoped MCP client-token lifetime (default 365)
	LLMModels                 []string // allowed models for the virtual key
	LLMMaxBudget              float64  // USD soft cap
	LLMRPMLimit               int      // requests/minute
}

func (c *Config) withDefaults() {
	if c.CertTTL == "" {
		c.CertTTL = "5m"
	}
	if c.CertMaxTTL == "" {
		c.CertMaxTTL = "30m"
	}
	if c.BoundaryTokenPeriod == "" {
		c.BoundaryTokenPeriod = "24h"
	}
	if c.MCPTokenDays == 0 {
		c.MCPTokenDays = 365
	}
	if c.GithubPluginVersion == "" {
		c.GithubPluginVersion = "2.3.2"
	}
	if len(c.LLMModels) == 0 {
		c.LLMModels = []string{"deepseek-v4-pro", "deepseek-v4-flash"}
	}
	if c.LLMMaxBudget == 0 {
		c.LLMMaxBudget = 50
	}
	if c.LLMRPMLimit == 0 {
		c.LLMRPMLimit = 120
	}
}

// Service orchestrates the per-project engine provision.
type Service struct {
	vault    blueprint.VaultAdmin
	boundary BoundaryCreds
	llm      LLMKeyGen
	gateway  mcpgw.Client // MCP gateway admin client for wiring MCP add-ons (may be nil)
	desc     DescriptorStore
	projects ProjectLookup
	audit    Auditor
	cfg      Config
}

// New builds the engine-provision service. gateway may be nil (MCP add-on wiring is
// then unavailable, but engine provision + extra-engine add-ons still work).
func New(vault blueprint.VaultAdmin, boundary BoundaryCreds, llm LLMKeyGen, gateway mcpgw.Client, desc DescriptorStore, projects ProjectLookup, audit Auditor, cfg Config) *Service {
	cfg.withDefaults()
	return &Service{vault: vault, boundary: boundary, llm: llm, gateway: gateway, desc: desc, projects: projects, audit: audit, cfg: cfg}
}

// ProvisionInput carries the project's GitHub App credentials. GithubAppPrivateKey
// is write-only: it goes to Vault (github/config) and is never persisted to Postgres
// or logs, nor echoed in the audit detail.
type ProvisionInput struct {
	GithubAppID             int      `json:"github_app_id"`
	GithubAppInstallationID int      `json:"github_app_installation_id"`
	GithubAppPrivateKey     string   `json:"github_app_private_key"`
	GithubRepositories      []string `json:"github_repositories,omitempty"`
}

const (
	sshMount      = "ssh"
	sshRole       = "dev-workspace"
	githubMount   = "github"
	githubPermSet = "dev-workspace"
	credStoreName = "vault"
	credLibName   = "dev-workspace-ssh-cert"
)

// Provision stands up the project's engines + Boundary credential objects and writes
// the resulting credential_library_id into the descriptor. Idempotent-forward.
// Membership on the project is enforced (project-admin path).
func (s *Service) Provision(ctx context.Context, actor string, groups []string, project string, in ProvisionInput) (descriptor.Descriptor, error) {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	return s.provision(ctx, actor, project, d, in)
}

// ProvisionAtCreate runs the full engine provision for a freshly-created project with
// EMPTY GitHub creds, reading the descriptor straight from the store (no membership
// check — the caller is the privileged project-create orchestrator). This makes a
// portal-created project's engines stand up automatically at create; a project-admin
// supplies the GitHub App creds later via SetGitHubCredentials.
func (s *Service) ProvisionAtCreate(ctx context.Context, actor, project string) (descriptor.Descriptor, error) {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	return s.provision(ctx, actor, project, d, ProvisionInput{})
}

// provision is the ordered, idempotent-forward engine build shared by the
// project-admin (Provision) and project-create (ProvisionAtCreate) entry points.
func (s *Service) provision(ctx context.Context, actor, project string, d descriptor.Descriptor, in ProvisionInput) (descriptor.Descriptor, error) {
	ns := d.Namespace
	wsUser := d.WorkspaceUser
	if wsUser == "" {
		wsUser = "dev"
	}

	// 1. SSH CA engine.
	if err := ignoreExists(s.vault.MountEngine(ctx, ns, sshMount, "ssh", "")); err != nil {
		return s.failf(ctx, actor, project, "ssh.mount", err)
	}
	if err := s.vault.WriteSSHCA(ctx, ns, sshMount); err != nil {
		return s.failf(ctx, actor, project, "ssh.ca", err)
	}
	if err := s.vault.WriteSSHRole(ctx, ns, sshMount, sshRole, blueprint.SSHRole{
		AllowedUsers: wsUser, DefaultUser: wsUser, TTL: s.cfg.CertTTL, MaxTTL: s.cfg.CertMaxTTL,
	}); err != nil {
		return s.failf(ctx, actor, project, "ssh.role", err)
	}

	// 2. GitHub App broker (external plugin). The mount is ALWAYS created (so the
	// workspace's github/token path exists), but the App config (app_id + private
	// key) is OPTIONAL here: at project-create time no creds are supplied, and a
	// project-admin sets/updates them later via SetGitHubCredentials. git push simply
	// won't work until the creds are set.
	if err := ignoreExists(s.vault.MountEngine(ctx, ns, githubMount, "vault-plugin-secrets-github", "v"+s.cfg.GithubPluginVersion)); err != nil {
		return s.failf(ctx, actor, project, "github.mount", err)
	}
	githubConfigured := hasGithubCreds(in)
	if githubConfigured {
		if err := s.writeGitHubConfig(ctx, ns, in.GithubAppID, in.GithubAppInstallationID, in.GithubAppPrivateKey, in.GithubRepositories); err != nil {
			return s.failf(ctx, actor, project, "github.config", err)
		}
	}

	// 3. Workspace WIF read policies (5). Names already carried by the workspace WIF
	// role (SeedWorkspaceRole); this writes their content.
	for name, hcl := range workspaceReadPolicies(project) {
		if err := s.vault.WritePolicy(ctx, ns, name, hcl); err != nil {
			return s.failf(ctx, actor, project, "wif-policy."+name, err)
		}
	}

	// 4. LiteLLM virtual key → secret/projects/llm (the workspace reads virtual_key).
	// Free any stale alias from a prior partial run first (best-effort) so re-provision
	// converges — LiteLLM requires unique aliases and 400s on a colliding generate.
	_ = s.llm.DeleteKeyByAlias(ctx, "llm-"+project)
	key, err := s.llm.GenerateKey(ctx, llmgw.KeySpec{
		Alias: "llm-" + project, Models: s.cfg.LLMModels,
		MaxBudget: s.cfg.LLMMaxBudget, RPMLimit: s.cfg.LLMRPMLimit,
		Metadata: map[string]string{"project": project},
	})
	if err != nil {
		return s.failf(ctx, actor, project, "llm.key", err)
	}
	if err := s.vault.WriteKVv2(ctx, ns, "secret", "projects/llm", map[string]any{
		"base_url": s.cfg.LLMGatewayPrivateEndpoint, "virtual_key": key,
	}); err != nil {
		return s.failf(ctx, actor, project, "llm.kv", err)
	}

	// 5. Boundary: cred-store policy → periodic token → Vault cred store → ssh-cert library.
	if err := s.vault.WritePolicy(ctx, ns, "boundary-cred-store", boundaryCredStorePolicy); err != nil {
		return s.failf(ctx, actor, project, "boundary.policy", err)
	}
	token, err := s.vault.CreatePeriodicToken(ctx, ns, []string{"boundary-cred-store"}, s.cfg.BoundaryTokenPeriod)
	if err != nil {
		return s.failf(ctx, actor, project, "boundary.token", err)
	}
	storeID, err := s.boundary.CreateVaultCredentialStore(ctx, d.ProjectScopeID, credStoreName, s.cfg.VaultCredStoreAddress, ns, token)
	if err != nil {
		return s.failf(ctx, actor, project, "boundary.cred-store", err)
	}
	libID, err := s.boundary.CreateSSHCertLibrary(ctx, storeID, credLibName, sshMount+"/sign/"+sshRole, wsUser, "{{.User.Email}}")
	if err != nil {
		return s.failf(ctx, actor, project, "boundary.ssh-cert-library", err)
	}

	// 6. Persist credential_library_id (+ github-configured flag and the non-secret
	// App coordinates) into the descriptor + mark ready.
	if githubConfigured {
		if err := s.setGithubDescriptor(ctx, project, in); err != nil {
			return s.failf(ctx, actor, project, "descriptor.github", err)
		}
	}
	out, err := s.writeCredentialLibrary(ctx, project, libID, githubConfigured)
	if err != nil {
		return s.failf(ctx, actor, project, "descriptor.write", err)
	}
	s.record(ctx, actor, project, "ok", map[string]any{"credential_library_id": libID, "github_configured": githubConfigured})
	return out, nil
}

// SetGitHubCredentials writes/updates the project's GitHub App config + permission
// set on the pre-existing github mount (created by Provision at project-create). It
// is the project-admin's deferred-credentials path. The private key is write-only.
func (s *Service) SetGitHubCredentials(ctx context.Context, actor string, groups []string, project string, in ProvisionInput) error {
	d, err := s.projects.GetProject(ctx, project, groups)
	if err != nil {
		return err
	}
	if !hasGithubCreds(in) {
		return fmt.Errorf("github_app_id, github_app_installation_id and github_app_private_key are required: %w", apperr.ErrBadRequest)
	}
	ns := d.Namespace
	// The mount normally exists (Provision created it at project-create); ensure it
	// idempotently so this path also works if provision was skipped.
	if err := ignoreExists(s.vault.MountEngine(ctx, ns, githubMount, "vault-plugin-secrets-github", "v"+s.cfg.GithubPluginVersion)); err != nil {
		s.record(ctx, actor, project, "error", map[string]any{"step": "github.mount"})
		return err
	}
	if err := s.writeGitHubConfig(ctx, ns, in.GithubAppID, in.GithubAppInstallationID, in.GithubAppPrivateKey, in.GithubRepositories); err != nil {
		s.record(ctx, actor, project, "error", map[string]any{"step": "github.config"})
		return err
	}
	if err := s.setGithubDescriptor(ctx, project, in); err != nil {
		return err
	}
	s.record(ctx, actor, project, "ok", map[string]any{"action": "github-credentials"})
	return nil
}

// EngineStatus is the non-secret provisioning state a project-admin sees. The
// GitHub App coordinates are echoed back so the Engines form can prefill; the
// private key is write-only and never part of this.
type EngineStatus struct {
	Provisioned             bool     `json:"provisioned"`       // engines stood up + descriptor ready
	GithubConfigured        bool     `json:"github_configured"` // GitHub App creds set
	GithubAppID             int      `json:"github_app_id,omitempty"`
	GithubAppInstallationID int      `json:"github_app_installation_id,omitempty"`
	GithubRepositories      []string `json:"github_repositories,omitempty"`
	CredentialLibraryID     string   `json:"credential_library_id"` // Boundary ssh-cert library
	Status                  string   `json:"status"`                // descriptor status (ready|error|provisioning)
}

// Status reports the project's engine-provisioning state from the descriptor.
func (s *Service) Status(ctx context.Context, groups []string, project string) (EngineStatus, error) {
	if _, err := s.projects.GetProject(ctx, project, groups); err != nil {
		return EngineStatus{}, err
	}
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return EngineStatus{}, err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return EngineStatus{}, err
	}
	return EngineStatus{
		Provisioned:             d.CredentialLibraryID != "" && pd.Status == store.StatusReady,
		GithubConfigured:        d.GithubConfigured,
		GithubAppID:             d.GithubAppID,
		GithubAppInstallationID: d.GithubAppInstallationID,
		GithubRepositories:      d.GithubRepositories,
		CredentialLibraryID:     d.CredentialLibraryID,
		Status:                  pd.Status,
	}, nil
}

// namespaceOf reads the project's Vault namespace from the descriptor store (no
// membership check — the caller, the template add-ons plane, already enforced it).
func (s *Service) namespaceOf(ctx context.Context, project string) (string, error) {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return "", err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return "", err
	}
	return d.Namespace, nil
}

// EnsureEngine idempotently mounts an extra secret engine in the project namespace
// (an add-on engine a project-admin wires into a workspace template). The workspace
// reads its secrets over WIF via the injected template blocks.
func (s *Service) EnsureEngine(ctx context.Context, project, mount, engineType string) error {
	if strings.TrimSpace(mount) == "" || strings.TrimSpace(engineType) == "" {
		return fmt.Errorf("engine mount and type are required: %w", apperr.ErrBadRequest)
	}
	ns, err := s.namespaceOf(ctx, project)
	if err != nil {
		return err
	}
	return ignoreExists(s.vault.MountEngine(ctx, ns, mount, engineType, ""))
}

// WireMCPServer makes a deployed MCP server consumable by the project's workspaces:
// it registers the server as a gateway peer, composes a per-project virtual server
// bundling its tools, mints a scoped client token, and writes {url, token} to the
// project's per-server MCP KV path (secret/data/projects/mcp/<name>) over the broker.
// The workspace's injected template reads that path at launch. Ports the retired
// terraform/project mcp-provision.sh. Best-effort idempotent (already-exists swallowed).
func (s *Service) WireMCPServer(ctx context.Context, project, serverName string) error {
	if s.gateway == nil {
		return fmt.Errorf("MCP gateway not configured: %w", apperr.ErrBadRequest)
	}
	ns, err := s.namespaceOf(ctx, project)
	if err != nil {
		return err
	}
	row, err := s.desc.GetProjectMCPServer(ctx, project, serverName)
	if err != nil {
		return err
	}
	// Same canonical name the deploy plane (projectadmin Test) registers the peer
	// under: ContextForge dedupes gateways by URL, so a different name here misses
	// RegisterPeer's by-name reuse and the POST 409s ("Gateway already exists").
	peerName := "mcp-" + project + "-" + serverName
	peerID, err := s.gateway.RegisterPeer(ctx, peerName, row.GatewayURL, row.Transport)
	if err != nil {
		return fmt.Errorf("register gateway peer: %w", err)
	}
	toolIDs, err := s.gateway.DiscoverTools(ctx, peerID)
	if err != nil {
		return fmt.Errorf("discover tools: %w", err)
	}
	vsID, err := s.gateway.CreateVirtualServer(ctx, peerName, "workspace MCP server "+serverName+" for "+project, toolIDs)
	if err != nil {
		return fmt.Errorf("create virtual server: %w", err)
	}
	// ContextForge reserves token names FOREVER (even after revoke — verified
	// live), so a fixed "<peer>-client" name 400s on any re-wire (e.g. after a
	// server delete+redeploy). Revoke prior client tokens, then mint under a fresh
	// random suffix; consumers read the token VALUE from KV, never the name.
	if err := s.gateway.RevokeTokensByPrefix(ctx, peerName+"-client"); err != nil {
		return fmt.Errorf("revoke stale client tokens: %w", err)
	}
	suffix, err := randHex(4)
	if err != nil {
		return err
	}
	token, err := s.gateway.CreateScopedToken(ctx, peerName+"-client-"+suffix, s.cfg.MCPTokenDays, vsID)
	if err != nil {
		return fmt.Errorf("create scoped token: %w", err)
	}
	url := strings.TrimRight(s.cfg.MCPGatewayEndpoint, "/") + "/servers/" + vsID + "/sse"
	if err := s.vault.WriteKVv2(ctx, ns, "secret", "projects/mcp/"+serverName, map[string]any{
		"url": url, "token": token,
	}); err != nil {
		return fmt.Errorf("write mcp kv: %w", err)
	}
	return nil
}

// randHex returns n random bytes hex-encoded (2n chars) — the per-wire token-name
// suffix that sidesteps ContextForge's forever-reserved token names.
func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hasGithubCreds reports whether a full GitHub App credential set was supplied.
func hasGithubCreds(in ProvisionInput) bool {
	return in.GithubAppID != 0 && in.GithubAppInstallationID != 0 && strings.TrimSpace(in.GithubAppPrivateKey) != ""
}

// writeGitHubConfig writes the App config (write-only key) + the dev-workspace
// permission set (contents:write, scoped to the given repos).
func (s *Service) writeGitHubConfig(ctx context.Context, ns string, appID, installID int, pem string, repos []string) error {
	if err := s.vault.WriteGitHubConfig(ctx, ns, githubMount, appID, pem); err != nil {
		return err
	}
	return s.vault.WriteGitHubPermissionSet(ctx, ns, githubMount, githubPermSet, installID,
		map[string]string{"contents": "write"}, normalizeRepos(repos))
}

// normalizeRepos reduces each repository entry to the bare repo NAME the GitHub
// App API expects (the owner is implied by the installation). Accepts what admins
// naturally paste: full URLs ("https://github.com/owner/repo.git"), "owner/repo",
// SSH remotes ("git@github.com:owner/repo.git"), or already-bare names. A
// URL-shaped entry passed through verbatim makes every token mint 422 and blocks
// workspace startup at the git-token template.
func normalizeRepos(repos []string) []string {
	out := make([]string, 0, len(repos))
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		r = strings.TrimSuffix(r, "/")
		r = strings.TrimSuffix(r, ".git")
		if i := strings.LastIndexAny(r, "/:"); i >= 0 {
			r = r[i+1:]
		}
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}

// setGithubDescriptor flips the descriptor's non-secret GithubConfigured flag and
// persists the non-secret App coordinates (app id, installation id, repos) so the
// Engines page can prefill its form. Never the private key.
func (s *Service) setGithubDescriptor(ctx context.Context, project string, in ProvisionInput) error {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return err
	}
	d.GithubConfigured = true
	d.GithubAppID = in.GithubAppID
	d.GithubAppInstallationID = in.GithubAppInstallationID
	// Persist the NORMALIZED names so the Engines form prefill shows what the
	// permission set actually contains.
	d.GithubRepositories = normalizeRepos(in.GithubRepositories)
	js, err := json.Marshal(d)
	if err != nil {
		return err
	}
	pd.Descriptor = js
	_, err = s.desc.UpsertProjectDescriptor(ctx, pd)
	return err
}

// writeCredentialLibrary reads the descriptor, sets CredentialLibraryID (+ OR-s in the
// github-configured flag), marks the row ready, and writes it back — preserving every
// other descriptor field.
func (s *Service) writeCredentialLibrary(ctx context.Context, project, libID string, githubConfigured bool) (descriptor.Descriptor, error) {
	pd, err := s.desc.GetProjectDescriptor(ctx, project)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	d, err := descriptor.Parse(string(pd.Descriptor))
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	d.CredentialLibraryID = libID
	d.GithubConfigured = d.GithubConfigured || githubConfigured
	js, err := json.Marshal(d)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	pd.Descriptor = js
	pd.Status = store.StatusReady
	if _, err := s.desc.UpsertProjectDescriptor(ctx, pd); err != nil {
		return descriptor.Descriptor{}, err
	}
	return d, nil
}

// failf marks the descriptor errored (best-effort), audits, and returns the wrapped
// error. Re-run is the recovery path (idempotent-forward), matching projectbootstrap.
func (s *Service) failf(ctx context.Context, actor, project, step string, err error) (descriptor.Descriptor, error) {
	if pd, gerr := s.desc.GetProjectDescriptor(ctx, project); gerr == nil {
		pd.Status = store.StatusError
		_, _ = s.desc.UpsertProjectDescriptor(ctx, pd)
	}
	s.record(ctx, actor, project, "error", map[string]any{"step": step, "error": err.Error()})
	return descriptor.Descriptor{}, fmt.Errorf("engine provision failed at %s: %w", step, err)
}

func (s *Service) record(ctx context.Context, actor, project, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	_ = s.audit.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: "project-engines.provision", Target: project, Outcome: outcome, Detail: detail})
}

// workspaceReadPolicies is the content of the 5 per-project WIF read policies (ports
// the vault_policy.nomad_* resources). Paths are namespace-local.
func workspaceReadPolicies(project string) map[string]string {
	read := func(path string) string {
		return fmt.Sprintf("path %q {\n  capabilities = [\"read\"]\n}\n", path)
	}
	return map[string]string{
		"nomad-" + project + "-ca-read":      read("ssh/config/ca"),
		"nomad-" + project + "-github-token": read("github/token/" + githubPermSet),
		"nomad-" + project + "-db-creds":     read("database/creds/dev-workspace-ro"),
		"nomad-" + project + "-llm-read":     read("secret/data/projects/llm"),
		// MCP wiring is per-server (secret/projects/mcp/<name>, written by
		// WireMCPServer) — grant the subtree, not just the legacy single blob.
		"nomad-" + project + "-mcp-read": read("secret/data/projects/mcp") + read("secret/data/projects/mcp/*"),
	}
}

// boundaryCredStorePolicy ports vault_policy.boundary: token self-lifecycle + lease
// paths + create/update on the project's SSH signing endpoint.
const boundaryCredStorePolicy = `path "auth/token/lookup-self" { capabilities = ["read"] }
path "auth/token/renew-self" { capabilities = ["update"] }
path "auth/token/revoke-self" { capabilities = ["update"] }
path "sys/leases/renew" { capabilities = ["update"] }
path "sys/leases/revoke" { capabilities = ["update"] }
path "sys/capabilities-self" { capabilities = ["update"] }
path "ssh/sign/dev-workspace" { capabilities = ["create", "update"] }
`

// ignoreExists swallows the "already in use"/"already exists" errors a re-mount
// returns so provisioning is idempotent-forward.
func ignoreExists(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "already in use") || strings.Contains(msg, "already exists") {
		return nil
	}
	return err
}
