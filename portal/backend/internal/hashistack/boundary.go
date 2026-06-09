package hashistack

import (
	"context"
	"fmt"
	"strings"

	bapi "github.com/hashicorp/boundary/api"
	"github.com/hashicorp/boundary/api/aliases"
	"github.com/hashicorp/boundary/api/authmethods"
	"github.com/hashicorp/boundary/api/hostcatalogs"
	"github.com/hashicorp/boundary/api/hosts"
	"github.com/hashicorp/boundary/api/hostsets"
	"github.com/hashicorp/boundary/api/managedgroups"
	"github.com/hashicorp/boundary/api/roles"
	"github.com/hashicorp/boundary/api/targets"
)

// Boundary wraps the Boundary controller API. The portal authenticates as the
// admin password account and, per workspace, creates the per-workspace resources
// (a static host catalog/host/host-set, an ssh target with injected SSH-cert
// credentials, and an alias) while wiring access through PER-DEVELOPER objects: a
// single OIDC managed group (email-filtered) and a single project-scope role bound
// to it, to which each workspace only ADDS an authorize-session grant for its
// target. One email => one managed group (so membership, evaluated at OIDC login,
// is stable and needs no re-auth after each create) and one role whose grant set
// tracks the developer's live targets.
type Boundary struct {
	c *bapi.Client
}

// NewBoundary builds a client and authenticates with the admin password method.
func NewBoundary(ctx context.Context, addr, authMethodID, login, password string, tls TLSOptions) (*Boundary, error) {
	c, err := bapi.NewClient(&bapi.Config{Addr: addr})
	if err != nil {
		return nil, fmt.Errorf("boundary: new client: %w", err)
	}
	// bapi.NewClient does not apply Config.TLSConfig; SetTLSConfig calls
	// ConfigureTLS so the CA cert / skip-verify actually take effect on the
	// transport (otherwise the self-signed loopback cert fails verification).
	if err := c.SetTLSConfig(&bapi.TLSConfig{CACert: tls.CACertPath, Insecure: tls.SkipVerify}); err != nil {
		return nil, fmt.Errorf("boundary: configure tls: %w", err)
	}
	res, err := authmethods.NewClient(c).Authenticate(ctx, authMethodID, "login", map[string]any{
		"login_name": login,
		"password":   password,
	})
	if err != nil {
		return nil, fmt.Errorf("boundary: authenticate: %w", err)
	}
	tok, err := res.GetAuthToken()
	if err != nil {
		return nil, fmt.Errorf("boundary: read auth token: %w", err)
	}
	c.SetToken(tok.Token)
	return &Boundary{c: c}, nil
}

// ProvisionInput carries everything needed to wire one workspace's access.
type ProvisionInput struct {
	ScopeID             string // project scope
	Name                string // ws-<handle>-<workspace>
	HostAddress         string // node private IP
	DefaultPort         uint32 // allocated SSH port
	CredentialLibraryID string // shared per-project SSH-cert library
	SessionMaxSeconds   uint32
	OIDCAuthMethodID    string // for the managed group
	DeveloperEmail      string // managed-group filter value
	DeveloperHandle     string // stable per-developer key; names the shared group/role
	AliasValue          string // <ws>.<handle>.<project>.<suffix>
}

// ProvisionResult reports the ids the UI needs to build connection configs.
type ProvisionResult struct {
	TargetID   string
	AliasValue string
}

// Provision creates the full per-workspace Boundary graph.
func (b *Boundary) Provision(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	hc, err := hostcatalogs.NewClient(b.c).Create(ctx, "static", in.ScopeID, hostcatalogs.WithName(in.Name))
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: create host catalog: %w", err)
	}
	host, err := hosts.NewClient(b.c).Create(ctx, hc.Item.Id, hosts.WithStaticHostAddress(in.HostAddress), hosts.WithName(in.Name))
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: create host: %w", err)
	}
	hs, err := hostsets.NewClient(b.c).Create(ctx, hc.Item.Id, hostsets.WithName(in.Name))
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: create host set: %w", err)
	}
	if _, err := hostsets.NewClient(b.c).AddHosts(ctx, hs.Item.Id, 0, []string{host.Item.Id}, hostsets.WithAutomaticVersioning(true)); err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: add host to set: %w", err)
	}

	tgt, err := targets.NewClient(b.c).Create(ctx, "ssh", in.ScopeID,
		targets.WithName(in.Name),
		targets.WithSshTargetDefaultPort(in.DefaultPort),
		targets.WithSessionConnectionLimit(-1),
		targets.WithSessionMaxSeconds(in.SessionMaxSeconds),
	)
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: create ssh target: %w", err)
	}
	if _, err := targets.NewClient(b.c).AddHostSources(ctx, tgt.Item.Id, 0, []string{hs.Item.Id}, targets.WithAutomaticVersioning(true)); err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: add host source to target: %w", err)
	}
	// Boundary ignores credential sources on target create (they're a sub-resource,
	// like host sources), so attach the injected SSH-cert library explicitly — this
	// is what makes the worker inject the Vault-signed cert at session time.
	if _, err := targets.NewClient(b.c).AddCredentialSources(ctx, tgt.Item.Id, 0,
		targets.WithInjectedApplicationCredentialSourceIds([]string{in.CredentialLibraryID}),
		targets.WithAutomaticVersioning(true)); err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: add injected credential source to target: %w", err)
	}

	// Per-developer managed group (one per email), shared by all the developer's
	// workspaces. Created once; OIDC login then resolves its membership.
	mgID, err := b.findOrCreateManagedGroup(ctx, in.OIDCAuthMethodID, "developer-"+in.DeveloperHandle, in.DeveloperEmail)
	if err != nil {
		return ProvisionResult{}, err
	}

	// Per-developer project-scope role bound to that group. This workspace only ADDS
	// an authorize-session grant for its target; Destroy removes just that grant.
	roleID, err := b.findOrCreateRole(ctx, in.ScopeID, "developer-"+in.DeveloperHandle, mgID, "this")
	if err != nil {
		return ProvisionResult{}, err
	}
	if err := b.ensureGrant(ctx, roleID, fmt.Sprintf("ids=%s;actions=authorize-session,read", tgt.Item.Id)); err != nil {
		return ProvisionResult{}, err
	}

	// Per-developer global role letting them resolve their own aliases. The grant is
	// identical for all their workspaces, so ensureGrant adds it just once.
	aliasRoleID, err := b.findOrCreateRole(ctx, "global", "resolve-aliases-"+in.DeveloperHandle, mgID, "this")
	if err != nil {
		return ProvisionResult{}, err
	}
	if err := b.ensureGrant(ctx, aliasRoleID, "ids={{.User.Id}};type=user;actions=list-resolvable-aliases"); err != nil {
		return ProvisionResult{}, err
	}

	if _, err := aliases.NewClient(b.c).Create(ctx, "target", "global",
		aliases.WithValue(in.AliasValue),
		aliases.WithDestinationId(tgt.Item.Id),
	); err != nil {
		return ProvisionResult{}, fmt.Errorf("boundary: create alias: %w", err)
	}

	return ProvisionResult{TargetID: tgt.Item.Id, AliasValue: in.AliasValue}, nil
}

// DestroyInput identifies the per-workspace Boundary graph to remove.
type DestroyInput struct {
	ScopeID         string // project scope
	Name            string // ws-<handle>-<workspace>
	DeveloperHandle string // names the per-developer role whose grant we drop
	AliasValue      string // <ws>.<handle>.<project>.<suffix>
}

// Destroy removes this workspace's per-workspace resources (target, host catalog,
// alias) and drops just this target's authorize-session grant from the developer's
// shared role. The managed group and the per-developer roles are left in place —
// they are reused by the developer's other workspaces. Best-effort and idempotent:
// resources are matched by name/id and skipped if already gone.
func (b *Boundary) Destroy(ctx context.Context, in DestroyInput) error {
	// Find this workspace's target so we can drop its grant and delete it.
	tgts, err := targets.NewClient(b.c).List(ctx, in.ScopeID)
	if err != nil {
		return fmt.Errorf("boundary: list targets: %w", err)
	}
	var targetID string
	for _, t := range tgts.Items {
		if t.Name == in.Name {
			targetID = t.Id
		}
	}
	if targetID != "" {
		// Remove only this target's grant from the per-developer role (kept intact).
		roleID, err := b.findRoleByName(ctx, in.ScopeID, "developer-"+in.DeveloperHandle)
		if err != nil {
			return err
		}
		if roleID != "" {
			if err := b.removeTargetGrant(ctx, roleID, targetID); err != nil {
				return err
			}
		}
		if _, err := targets.NewClient(b.c).Delete(ctx, targetID); err != nil {
			return fmt.Errorf("boundary: delete target: %w", err)
		}
	}

	hcs, err := hostcatalogs.NewClient(b.c).List(ctx, in.ScopeID)
	if err != nil {
		return fmt.Errorf("boundary: list host catalogs: %w", err)
	}
	for _, hc := range hcs.Items {
		if hc.Name == in.Name {
			if _, err := hostcatalogs.NewClient(b.c).Delete(ctx, hc.Id); err != nil {
				return fmt.Errorf("boundary: delete host catalog: %w", err)
			}
		}
	}

	als, err := aliases.NewClient(b.c).List(ctx, "global")
	if err != nil {
		return fmt.Errorf("boundary: list aliases: %w", err)
	}
	for _, a := range als.Items {
		if a.Value == in.AliasValue {
			if _, err := aliases.NewClient(b.c).Delete(ctx, a.Id); err != nil {
				return fmt.Errorf("boundary: delete alias: %w", err)
			}
		}
	}
	return nil
}

// findOrCreateManagedGroup returns the id of the per-developer OIDC managed group
// named name (email-filtered), creating it if absent. One per developer: the email
// filter matches the same account at every OIDC login.
func (b *Boundary) findOrCreateManagedGroup(ctx context.Context, oidcAuthMethodID, name, email string) (string, error) {
	mc := managedgroups.NewClient(b.c)
	list, err := mc.List(ctx, oidcAuthMethodID)
	if err != nil {
		return "", fmt.Errorf("boundary: list managed groups: %w", err)
	}
	for _, mg := range list.Items {
		if mg.Name == name {
			return mg.Id, nil
		}
	}
	mg, err := mc.Create(ctx, oidcAuthMethodID,
		managedgroups.WithName(name),
		managedgroups.WithOidcManagedGroupFilter(fmt.Sprintf("%q == %q", "/token/email", email)),
	)
	if err != nil {
		return "", fmt.Errorf("boundary: create managed group %q: %w", name, err)
	}
	return mg.Item.Id, nil
}

// findOrCreateRole returns the id of the role named name in scopeID, creating it
// (grant scope grantScope, principal = managedGroupID) if absent. Grants are added
// separately so the role's grant set can track per-workspace targets.
func (b *Boundary) findOrCreateRole(ctx context.Context, scopeID, name, managedGroupID, grantScope string) (string, error) {
	rc := roles.NewClient(b.c)
	id, err := b.findRoleByName(ctx, scopeID, name)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	r, err := rc.Create(ctx, scopeID, roles.WithName(name))
	if err != nil {
		return "", fmt.Errorf("boundary: create role %q: %w", name, err)
	}
	if _, err := rc.SetGrantScopes(ctx, r.Item.Id, 0, []string{grantScope}, roles.WithAutomaticVersioning(true)); err != nil {
		return "", fmt.Errorf("boundary: set grant scope on role %q: %w", name, err)
	}
	if _, err := rc.AddPrincipals(ctx, r.Item.Id, 0, []string{managedGroupID}, roles.WithAutomaticVersioning(true)); err != nil {
		return "", fmt.Errorf("boundary: add principal to role %q: %w", name, err)
	}
	return r.Item.Id, nil
}

// ensureGrant adds grant to roleID if not already present (idempotent, so a grant
// shared across a developer's workspaces is added only once).
func (b *Boundary) ensureGrant(ctx context.Context, roleID, grant string) error {
	rc := roles.NewClient(b.c)
	r, err := rc.Read(ctx, roleID)
	if err != nil {
		return fmt.Errorf("boundary: read role %q: %w", roleID, err)
	}
	for _, g := range r.Item.GrantStrings {
		if g == grant {
			return nil
		}
	}
	if _, err := rc.AddGrants(ctx, roleID, 0, []string{grant}, roles.WithAutomaticVersioning(true)); err != nil {
		return fmt.Errorf("boundary: add grant to role %q: %w", roleID, err)
	}
	return nil
}

// findRoleByName returns the id of the role named name in scopeID, or "" if none.
func (b *Boundary) findRoleByName(ctx context.Context, scopeID, name string) (string, error) {
	list, err := roles.NewClient(b.c).List(ctx, scopeID)
	if err != nil {
		return "", fmt.Errorf("boundary: list roles in %q: %w", scopeID, err)
	}
	for _, r := range list.Items {
		if r.Name == name {
			return r.Id, nil
		}
	}
	return "", nil
}

// removeTargetGrant removes the grant referencing targetID from roleID (matched by
// the target id, so grant-string canonicalization differences don't matter).
func (b *Boundary) removeTargetGrant(ctx context.Context, roleID, targetID string) error {
	rc := roles.NewClient(b.c)
	r, err := rc.Read(ctx, roleID)
	if err != nil {
		return fmt.Errorf("boundary: read role %q: %w", roleID, err)
	}
	for _, g := range r.Item.GrantStrings {
		if strings.Contains(g, targetID) {
			if _, err := rc.RemoveGrants(ctx, roleID, 0, []string{g}, roles.WithAutomaticVersioning(true)); err != nil {
				return fmt.Errorf("boundary: remove grant from role %q: %w", roleID, err)
			}
		}
	}
	return nil
}

// TargetIDsByPrefix returns name->id for ssh targets in scopeID whose name
// starts with prefix (used to attach connection info to listed workspaces).
func (b *Boundary) TargetIDsByPrefix(ctx context.Context, scopeID, prefix string) (map[string]string, error) {
	list, err := targets.NewClient(b.c).List(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("boundary: list targets: %w", err)
	}
	out := map[string]string{}
	for _, t := range list.Items {
		if len(t.Name) >= len(prefix) && t.Name[:len(prefix)] == prefix {
			out[t.Name] = t.Id
		}
	}
	return out, nil
}
