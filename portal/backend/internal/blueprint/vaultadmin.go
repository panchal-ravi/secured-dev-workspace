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

// vaultAdmin is the live implementation over a *vapi.Client.
type vaultAdmin struct{ c *vapi.Client }

// NewVaultAdmin wraps a configured Vault API client.
func NewVaultAdmin(c *vapi.Client) VaultAdmin { return &vaultAdmin{c: c} }

func (a *vaultAdmin) ns(ns string) *vapi.Client { return a.c.WithNamespace(ns) }

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

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("vaultadmin: %s: %w", op, err)
}
