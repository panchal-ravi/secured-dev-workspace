package blueprint

import (
	"context"
	"fmt"

	vapi "github.com/hashicorp/vault/api"
)

// DBConnectionConfig configures a database secret engine connection.
type DBConnectionConfig struct {
	Plugin        string   // e.g. "postgresql-database-plugin"
	ConnectionURL string   // with {{username}}/{{password}} templating
	Username      string   // bootstrap admin user (rotated immediately after)
	Password      string   // bootstrap admin password (write-only param)
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
	CreateNamespace(ctx context.Context, path string) error
	DeleteNamespace(ctx context.Context, path string) error

	MountEngine(ctx context.Context, ns, path, engineType, pluginVersion string) error
	UnmountEngine(ctx context.Context, ns, path string) error

	EnableAuth(ctx context.Context, ns, path, authType string) error
	DisableAuth(ctx context.Context, ns, path string) error

	ConfigureDBConnection(ctx context.Context, ns, mount, name string, cfg DBConnectionConfig) error
	RotateRoot(ctx context.Context, ns, mount, name string) error
	WriteDBRole(ctx context.Context, ns, mount, name string, role DBRole) error

	WriteKVv2(ctx context.Context, ns, mount, relPath string, data map[string]any) error

	WritePolicy(ctx context.Context, ns, name, policyHCL string) error
	DeletePolicy(ctx context.Context, ns, name string) error

	WriteWIFRole(ctx context.Context, ns, authPath, roleName string, role WIFRole) error
	DeleteWIFRole(ctx context.Context, ns, authPath, roleName string) error

	RevokeLeasesByPrefix(ctx context.Context, ns, prefix string) error

	MintTokenWithPolicies(ctx context.Context, ns string, policies []string, ttl string) (string, error)
	// Read performs a token-scoped read; ok=false with no error means a clean 403
	// (the probe the Validator uses to assert denial).
	Read(ctx context.Context, ns, token, path string) (ok bool, err error)
}

// vaultAdmin is the live implementation over a *vapi.Client.
type vaultAdmin struct{ c *vapi.Client }

// NewVaultAdmin wraps a configured Vault API client.
func NewVaultAdmin(c *vapi.Client) VaultAdmin { return &vaultAdmin{c: c} }

func (a *vaultAdmin) ns(ns string) *vapi.Client { return a.c.WithNamespace(ns) }

func (a *vaultAdmin) CreateNamespace(ctx context.Context, path string) error {
	_, err := a.c.Logical().WriteWithContext(ctx, "sys/namespaces/"+path, nil)
	return wrap("create namespace", err)
}

func (a *vaultAdmin) DeleteNamespace(ctx context.Context, path string) error {
	_, err := a.c.Logical().DeleteWithContext(ctx, "sys/namespaces/"+path)
	return wrap("delete namespace", err)
}

func (a *vaultAdmin) MountEngine(ctx context.Context, ns, path, engineType, pluginVersion string) error {
	in := &vapi.MountInput{Type: engineType}
	if pluginVersion != "" {
		in.Options = map[string]string{"plugin_version": pluginVersion}
	}
	err := a.ns(ns).Sys().MountWithContext(ctx, path, in)
	return wrap("mount engine", err)
}

func (a *vaultAdmin) UnmountEngine(ctx context.Context, ns, path string) error {
	return wrap("unmount engine", a.ns(ns).Sys().UnmountWithContext(ctx, path))
}

func (a *vaultAdmin) EnableAuth(ctx context.Context, ns, path, authType string) error {
	return wrap("enable auth", a.ns(ns).Sys().EnableAuthWithOptionsWithContext(ctx, path, &vapi.EnableAuthOptions{Type: authType}))
}

func (a *vaultAdmin) DisableAuth(ctx context.Context, ns, path string) error {
	return wrap("disable auth", a.ns(ns).Sys().DisableAuthWithContext(ctx, path))
}

func (a *vaultAdmin) ConfigureDBConnection(ctx context.Context, ns, mount, name string, cfg DBConnectionConfig) error {
	_, err := a.ns(ns).Logical().WriteWithContext(ctx, mount+"/config/"+name, map[string]any{
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
	_, err := a.ns(ns).Logical().WriteWithContext(ctx, mount+"/rotate-root/"+name, nil)
	return wrap("rotate-root", err)
}

func (a *vaultAdmin) WriteDBRole(ctx context.Context, ns, mount, name string, role DBRole) error {
	_, err := a.ns(ns).Logical().WriteWithContext(ctx, mount+"/roles/"+name, map[string]any{
		"db_name":             role.DBName,
		"creation_statements": role.CreationStatements,
		"default_ttl":         role.DefaultTTLSeconds,
		"max_ttl":             role.MaxTTLSeconds,
	})
	return wrap("write db role", err)
}

func (a *vaultAdmin) WriteKVv2(ctx context.Context, ns, mount, relPath string, data map[string]any) error {
	_, err := a.ns(ns).KVv2(mount).Put(ctx, relPath, data)
	return wrap("write kv", err)
}

func (a *vaultAdmin) WritePolicy(ctx context.Context, ns, name, policyHCL string) error {
	return wrap("write policy", a.ns(ns).Sys().PutPolicyWithContext(ctx, name, policyHCL))
}

func (a *vaultAdmin) DeletePolicy(ctx context.Context, ns, name string) error {
	return wrap("delete policy", a.ns(ns).Sys().DeletePolicyWithContext(ctx, name))
}

func (a *vaultAdmin) WriteWIFRole(ctx context.Context, ns, authPath, roleName string, role WIFRole) error {
	_, err := a.ns(ns).Logical().WriteWithContext(ctx, "auth/"+authPath+"/role/"+roleName, map[string]any{
		"role_type":       "jwt",
		"bound_audiences": role.BoundAudiences,
		"user_claim":      role.UserClaim,
		"token_policies":  role.TokenPolicies,
		"token_ttl":       role.TokenTTL,
	})
	return wrap("write wif role", err)
}

func (a *vaultAdmin) DeleteWIFRole(ctx context.Context, ns, authPath, roleName string) error {
	_, err := a.ns(ns).Logical().DeleteWithContext(ctx, "auth/"+authPath+"/role/"+roleName)
	return wrap("delete wif role", err)
}

// RevokeLeasesByPrefix force-revokes every lease under prefix (the structural fix:
// always called before UnmountEngine so a mount never has live leases at unmount).
func (a *vaultAdmin) RevokeLeasesByPrefix(ctx context.Context, ns, prefix string) error {
	_, err := a.ns(ns).Logical().WriteWithContext(ctx, "sys/leases/revoke-force/"+prefix, nil)
	return wrap("revoke leases by prefix", err)
}

func (a *vaultAdmin) MintTokenWithPolicies(ctx context.Context, ns string, policies []string, ttl string) (string, error) {
	sec, err := a.ns(ns).Auth().Token().CreateWithContext(ctx, &vapi.TokenCreateRequest{
		Policies: policies, TTL: ttl, NoParent: true, NumUses: 0,
	})
	if err != nil {
		return "", wrap("mint token", err)
	}
	return sec.Auth.ClientToken, nil
}

func (a *vaultAdmin) Read(ctx context.Context, ns, token, path string) (bool, error) {
	c := a.ns(ns)
	c.SetToken(token)
	sec, err := c.Logical().ReadWithContext(ctx, path)
	if err != nil {
		if re, ok := err.(*vapi.ResponseError); ok && (re.StatusCode == 403 || re.StatusCode == 404) {
			return false, nil // clean denial / no data — not a transport error
		}
		return false, wrap("probe read", err)
	}
	return sec != nil, nil
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("vaultadmin: %s: %w", op, err)
}
