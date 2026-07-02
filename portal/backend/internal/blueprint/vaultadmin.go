package blueprint

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	vapi "github.com/hashicorp/vault/api"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// DBConnectionConfig configures a database secret engine connection.
type DBConnectionConfig struct {
	Plugin        string // e.g. "postgresql-database-plugin"
	ConnectionURL string // with {{username}}/{{password}} templating
	Username      string // bootstrap admin user (rotated immediately after)
	Password      string // bootstrap admin password (write-only param)
	AllowedRoles  []string
}

// DBRole is a dynamic database role.
type DBRole struct {
	DBName             string
	CreationStatements []string
	DefaultTTLSeconds  int
	MaxTTLSeconds      int
}

// SSHRole is a Vault SSH signing role (key_type=ca). Ports
// terraform/project/vault.tf's vault_ssh_secret_backend_role.dev_workspace: locks
// the cert to the workspace user, permits a pty + TCP port forwarding (required
// for VSCode Remote-SSH), short TTLs. Boundary stamps key_id = developer email.
type SSHRole struct {
	AllowedUsers string
	DefaultUser  string
	TTL          string // e.g. "5m"
	MaxTTL       string // e.g. "30m"
}

// WIFRole binds a Nomad-WIF jwt role to token policies (the generated policy).
type WIFRole struct {
	BoundAudiences []string
	UserClaim      string
	TokenPolicies  []string
	TokenTTL       string
}

// VaultAdmin is the privileged engine-lifecycle surface the blueprint engine needs.
// Every method takes an explicit namespace; the impl scopes the call with
// client.WithNamespace(ns). Faked in unit tests.
type VaultAdmin interface {
	MountEngine(ctx context.Context, ns, path, engineType, pluginVersion string) error
	UnmountEngine(ctx context.Context, ns, path string) error

	ConfigureDBConnection(ctx context.Context, ns, mount, name string, cfg DBConnectionConfig) error
	RotateRoot(ctx context.Context, ns, mount, name string) error
	WriteDBRole(ctx context.Context, ns, mount, name string, role DBRole) error

	// SSH CA engine (ssh secrets engine in signing/CA mode).
	WriteSSHCA(ctx context.Context, ns, mount string) error
	WriteSSHRole(ctx context.Context, ns, mount, name string, role SSHRole) error

	// GitHub App secrets engine (external plugin vault-plugin-secrets-github).
	// The private key is write-only (never read back, never persisted outside Vault).
	WriteGitHubConfig(ctx context.Context, ns, mount string, appID int, privKeyPEM string) error
	WriteGitHubPermissionSet(ctx context.Context, ns, mount, name string, installID int, perms map[string]string, repos []string) error

	// CreatePeriodicToken mints a periodic, orphan, renewable token bound to the
	// given namespace-local policies (the Boundary credential-store auth token).
	CreatePeriodicToken(ctx context.Context, ns string, policies []string, period string) (token string, err error)

	WriteKVv2(ctx context.Context, ns, mount, relPath string, data map[string]any) error

	WritePolicy(ctx context.Context, ns, name, policyHCL string) error
	DeletePolicy(ctx context.Context, ns, name string) error

	WriteWIFRole(ctx context.Context, ns, authPath, roleName string, role WIFRole) error
	DeleteWIFRole(ctx context.Context, ns, authPath, roleName string) error

	RevokeLeasesByPrefix(ctx context.Context, ns, prefix string) error
}

// ProvisionerConfig points the admin at the Nomad workload-identity JWT and the
// per-namespace auth role it exchanges for a namespace-native provisioning token.
// An empty JWTPath disables the broker (fails closed with ErrBadRequest).
type ProvisionerConfig struct {
	JWTPath   string // Nomad-written workload-identity JWT file (aud=vault-provisioner)
	Role      string // jwt-nomad role, e.g. "portal-provisioner"
	AuthMount string // auth mount, e.g. "jwt-nomad"
}

// vaultAdmin is the live implementation over a *vapi.Client. Every privileged call
// runs under a token NATIVE to the target project namespace, brokered via a
// Nomad-WIF JWT login and cached per namespace for its short TTL. The wrapped
// client's own (root-namespace) token is never used for project writes.
type vaultAdmin struct {
	c   *vapi.Client
	cfg ProvisionerConfig

	mu     sync.Mutex
	tokens map[string]brokeredToken // namespace -> cached provisioner token
}

type brokeredToken struct {
	token  string
	expiry time.Time
}

// NewVaultAdmin wraps a configured Vault API client with the provisioner-broker config.
func NewVaultAdmin(c *vapi.Client, cfg ProvisionerConfig) VaultAdmin {
	return &vaultAdmin{c: c, cfg: cfg, tokens: map[string]brokeredToken{}}
}

// ns returns a client scoped to namespace ns and authenticated with a brokered,
// namespace-native provisioner token (not the portal's root token).
func (a *vaultAdmin) ns(ctx context.Context, ns string) (*vapi.Client, error) {
	tok, err := a.brokerToken(ctx, ns)
	if err != nil {
		return nil, err
	}
	cl := a.c.WithNamespace(ns) // independent clone
	cl.SetToken(tok)
	return cl, nil
}

// brokerToken returns a valid provisioner token for ns, minting one via JWT login
// when the cache is empty or near expiry. The JWT is re-read from disk each login
// so identity rotation is picked up.
func (a *vaultAdmin) brokerToken(ctx context.Context, ns string) (string, error) {
	if a.cfg.JWTPath == "" {
		return "", fmt.Errorf("vaultadmin: provisioner broker not configured (project-deploy plane disabled): %w", apperr.ErrBadRequest)
	}
	// The login (a network call) runs under the lock deliberately: provisioning is a
	// low-concurrency, one-deploy-at-a-time path, so serializing logins is simpler than
	// a per-namespace lock/singleflight and the correctness is obvious. A token with
	// lease TTL <= 30s (the skew below) is never cached and re-minted each op.
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.tokens[ns]; ok && time.Until(b.expiry) > 30*time.Second {
		return b.token, nil
	}
	jwt, err := os.ReadFile(a.cfg.JWTPath)
	if err != nil {
		return "", fmt.Errorf("vaultadmin: read provisioner jwt: %w", err)
	}
	login := a.c.WithNamespace(ns)
	login.ClearToken()
	sec, err := login.Logical().WriteWithContext(ctx, "auth/"+a.cfg.AuthMount+"/login", map[string]any{
		"role": a.cfg.Role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return "", wrap("provisioner login", err)
	}
	if sec == nil || sec.Auth == nil || sec.Auth.ClientToken == "" {
		return "", fmt.Errorf("vaultadmin: provisioner login returned no token")
	}
	a.tokens[ns] = brokeredToken{
		token:  sec.Auth.ClientToken,
		expiry: time.Now().Add(time.Duration(sec.Auth.LeaseDuration) * time.Second),
	}
	return sec.Auth.ClientToken, nil
}

func (a *vaultAdmin) MountEngine(ctx context.Context, ns, path, engineType, pluginVersion string) error {
	in := &vapi.MountInput{Type: engineType}
	if pluginVersion != "" {
		in.Options = map[string]string{"plugin_version": pluginVersion}
	}
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	return wrap("mount engine", cl.Sys().MountWithContext(ctx, path, in))
}

func (a *vaultAdmin) UnmountEngine(ctx context.Context, ns, path string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	return wrap("unmount engine", cl.Sys().UnmountWithContext(ctx, path))
}

func (a *vaultAdmin) ConfigureDBConnection(ctx context.Context, ns, mount, name string, cfg DBConnectionConfig) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/config/"+name, map[string]any{
		"plugin_name":    cfg.Plugin,
		"connection_url": cfg.ConnectionURL,
		"username":       cfg.Username,
		"password":       cfg.Password,
		"allowed_roles":  cfg.AllowedRoles,
		// Vault verifies on first credential request; demo-db accepts TCP before ready.
		"verify_connection": false,
	})
	return wrap("configure db connection", err)
}

func (a *vaultAdmin) RotateRoot(ctx context.Context, ns, mount, name string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/rotate-root/"+name, nil)
	return wrap("rotate-root", err)
}

func (a *vaultAdmin) WriteDBRole(ctx context.Context, ns, mount, name string, role DBRole) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/roles/"+name, map[string]any{
		"db_name":             role.DBName,
		"creation_statements": role.CreationStatements,
		"default_ttl":         role.DefaultTTLSeconds,
		"max_ttl":             role.MaxTTLSeconds,
	})
	return wrap("write db role", err)
}

// WriteSSHCA generates + holds the CA signing key inside Vault (ed25519 so OpenSSH
// accepts the signature without deprecated ssh-rsa/SHA-1).
func (a *vaultAdmin) WriteSSHCA(ctx context.Context, ns, mount string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	// Idempotent: the CA is generate-once (Vault rejects a second generate_signing_key
	// while keys exist), so skip when already configured. This keeps re-provision safe —
	// a re-run after a mid-provision failure must converge, not error on the SSH CA.
	if sec, rerr := cl.Logical().ReadWithContext(ctx, mount+"/config/ca"); rerr == nil && sec != nil {
		if pk, _ := sec.Data["public_key"].(string); pk != "" {
			return nil
		}
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/config/ca", map[string]any{
		"generate_signing_key": true,
		"key_type":             "ed25519",
	})
	return wrap("write ssh ca", err)
}

func (a *vaultAdmin) WriteSSHRole(ctx context.Context, ns, mount, name string, role SSHRole) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/roles/"+name, map[string]any{
		"key_type":                "ca",
		"allow_user_certificates": true,
		"allow_user_key_ids":      true, // Boundary stamps key_id = developer email (audit)
		"allowed_users":           role.AllowedUsers,
		"default_user":            role.DefaultUser,
		"default_extensions": map[string]string{
			"permit-pty":             "",
			"permit-port-forwarding": "",
		},
		"ttl":     role.TTL,
		"max_ttl": role.MaxTTL,
	})
	return wrap("write ssh role", err)
}

// WriteGitHubConfig configures the GitHub App broker with the project's App id +
// private key. prv_key is write-only: never read back, never persisted outside Vault.
func (a *vaultAdmin) WriteGitHubConfig(ctx context.Context, ns, mount string, appID int, privKeyPEM string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/config", map[string]any{
		"app_id":  appID,
		"prv_key": privKeyPEM,
	})
	return wrap("write github config", err)
}

// WriteGitHubPermissionSet defines a pre-scoped installation-token set (installation
// id + minimal permissions, optionally repo-constrained). The workspace reads
// github/token/<name> with no params, so the scope never leaves Vault.
func (a *vaultAdmin) WriteGitHubPermissionSet(ctx context.Context, ns, mount, name string, installID int, perms map[string]string, repos []string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	data := map[string]any{
		"installation_id": installID,
		"permissions":     perms,
	}
	if len(repos) > 0 {
		data["repositories"] = repos
	}
	_, err = cl.Logical().WriteWithContext(ctx, mount+"/permissionset/"+name, data)
	return wrap("write github permissionset", err)
}

// CreatePeriodicToken mints a periodic, orphan, renewable token bound to policies —
// what Boundary authenticates to Vault with (it self-renews indefinitely; orphan so
// its lifecycle is independent of the brokered token that created it).
func (a *vaultAdmin) CreatePeriodicToken(ctx context.Context, ns string, policies []string, period string) (string, error) {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return "", err
	}
	// Vault forbids a non-root token from creating an orphan token or from assigning a
	// policy it does not itself hold ("child policies must be subset of parent"). A token
	// role authorizes both explicitly — without granting the provisioner blanket sudo:
	// allowed_policies whitelists the policy and orphan makes the token parentless. The
	// role name is deterministic per policy set and re-created idempotently; the period on
	// the role makes tokens minted through it periodic.
	// Boundary's Vault credential store requires a PERIODIC token; Vault requires sudo to
	// mint one. A token role does not by itself yield a periodic token here (its period is
	// applied as a renewable TTL, not a period), so the period is passed at create — the
	// provisioner policy scopes sudo to auth/token/create/periodic-* for exactly this. The
	// role's allowed_policies whitelist + orphan bound what the token may carry. The role
	// name is deterministic per policy set and re-created idempotently.
	role := "periodic-" + strings.Join(policies, "-")
	if _, err := cl.Logical().WriteWithContext(ctx, "auth/token/roles/"+role, map[string]any{
		"allowed_policies": policies,
		"orphan":           true,
		"renewable":        true,
		"token_type":       "service",
	}); err != nil {
		return "", wrap("create token role", err)
	}
	sec, err := cl.Logical().WriteWithContext(ctx, "auth/token/create/"+role, map[string]any{
		"policies": policies,
		"period":   period,
		"metadata": map[string]string{"purpose": "boundary-credential-store"},
	})
	if err != nil {
		return "", wrap("create periodic token", err)
	}
	if sec == nil || sec.Auth == nil || sec.Auth.ClientToken == "" {
		return "", fmt.Errorf("vaultadmin: create periodic token returned no token")
	}
	return sec.Auth.ClientToken, nil
}

func (a *vaultAdmin) WriteKVv2(ctx context.Context, ns, mount, relPath string, data map[string]any) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.KVv2(mount).Put(ctx, relPath, data)
	return wrap("write kv", err)
}

func (a *vaultAdmin) WritePolicy(ctx context.Context, ns, name, policyHCL string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	return wrap("write policy", cl.Sys().PutPolicyWithContext(ctx, name, policyHCL))
}

func (a *vaultAdmin) DeletePolicy(ctx context.Context, ns, name string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	return wrap("delete policy", cl.Sys().DeletePolicyWithContext(ctx, name))
}

func (a *vaultAdmin) WriteWIFRole(ctx context.Context, ns, authPath, roleName string, role WIFRole) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, "auth/"+authPath+"/role/"+roleName, map[string]any{
		"role_type":       "jwt",
		"bound_audiences": role.BoundAudiences,
		"user_claim":      role.UserClaim,
		"token_policies":  role.TokenPolicies,
		"token_ttl":       role.TokenTTL,
	})
	return wrap("write wif role", err)
}

func (a *vaultAdmin) DeleteWIFRole(ctx context.Context, ns, authPath, roleName string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().DeleteWithContext(ctx, "auth/"+authPath+"/role/"+roleName)
	return wrap("delete wif role", err)
}

// RevokeLeasesByPrefix force-revokes every lease under prefix (the structural fix:
// always called before UnmountEngine so a mount never has live leases at unmount).
func (a *vaultAdmin) RevokeLeasesByPrefix(ctx context.Context, ns, prefix string) error {
	cl, err := a.ns(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, "sys/leases/revoke-force/"+prefix, nil)
	return wrap("revoke leases by prefix", err)
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("vaultadmin: %s: %w", op, err)
}
