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
