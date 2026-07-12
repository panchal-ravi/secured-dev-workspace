// Package boundary is a thin Boundary API client for the host-sync: it resolves a
// project scope + its shared host catalog + a workspace's host-set by name, and
// creates/updates the single static host inside that set. All structural objects
// (scope, catalog, host-set, target) are owned by the portal; this client only
// manages the host address.
package boundary

import (
	"context"
	"fmt"
	"net/http"

	bapi "github.com/hashicorp/boundary/api"
	"github.com/hashicorp/boundary/api/authmethods"
	"github.com/hashicorp/boundary/api/hostcatalogs"
	"github.com/hashicorp/boundary/api/hosts"
	"github.com/hashicorp/boundary/api/hostsets"
	"github.com/hashicorp/boundary/api/scopes"
)

// Client wraps the Boundary controller API, authenticated as the admin password
// account (same as the portal). The auth-method credentials are retained so the
// client can re-authenticate when its session token expires — the job runs
// long-lived (no periodic restart) so the token would otherwise go stale.
type Client struct {
	c           *bapi.Client
	orgScopeID  string
	catalogName string

	authMethodID string
	login        string
	password     string
}

// New builds and authenticates a client. orgScopeID is the parent of all project
// scopes; catalogName is the per-project shared catalog name to resolve.
func New(ctx context.Context, addr, authMethodID, login, password, orgScopeID, catalogName, caCertPath string, skipVerify bool) (*Client, error) {
	c, err := bapi.NewClient(&bapi.Config{Addr: addr})
	if err != nil {
		return nil, fmt.Errorf("boundary: new client: %w", err)
	}
	if err := c.SetTLSConfig(&bapi.TLSConfig{CACert: caCertPath, Insecure: skipVerify}); err != nil {
		return nil, fmt.Errorf("boundary: configure tls: %w", err)
	}
	b := &Client{
		c:            c,
		orgScopeID:   orgScopeID,
		catalogName:  catalogName,
		authMethodID: authMethodID,
		login:        login,
		password:     password,
	}
	if err := b.authenticate(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

// authenticate logs in at the password auth method and sets the session token on the
// underlying client. Called once at construction and again on token expiry.
func (b *Client) authenticate(ctx context.Context) error {
	res, err := authmethods.NewClient(b.c).Authenticate(ctx, b.authMethodID, "login", map[string]any{
		"login_name": b.login,
		"password":   b.password,
	})
	if err != nil {
		return fmt.Errorf("boundary: authenticate: %w", err)
	}
	tok, err := res.GetAuthToken()
	if err != nil {
		return fmt.Errorf("boundary: read auth token: %w", err)
	}
	b.c.SetToken(tok.Token)
	return nil
}

// isAuthExpired reports whether err is a Boundary 401 (an expired/invalid session
// token) — the signal to re-authenticate and retry.
func isAuthExpired(err error) bool {
	if err == nil {
		return false
	}
	se := bapi.AsServerError(err)
	return se != nil && se.Response() != nil && se.Response().StatusCode() == http.StatusUnauthorized
}

// retryOnAuthExpiry runs fn; if it fails with a 401, it runs reauth once and retries
// fn a single time. reauth is separate so the retry policy is unit-testable without a
// live controller. Calls are serial (the reconcile loop is single-threaded), so no
// locking is needed around the token swap.
func retryOnAuthExpiry(fn func() error, reauth func() error) error {
	err := fn()
	if !isAuthExpired(err) {
		return err
	}
	if rerr := reauth(); rerr != nil {
		return fmt.Errorf("re-authenticate after 401: %w (original: %v)", rerr, err)
	}
	return fn()
}

// do wraps a Boundary operation with the re-auth-on-401 retry.
func (b *Client) do(ctx context.Context, fn func() error) error {
	return retryOnAuthExpiry(fn, func() error { return b.authenticate(ctx) })
}

// ScopeIDByName returns the id of the project scope named name (a direct child of the
// org scope), or ok=false if absent.
func (b *Client) ScopeIDByName(ctx context.Context, name string) (string, bool, error) {
	var id string
	var ok bool
	err := b.do(ctx, func() error {
		list, err := scopes.NewClient(b.c).List(ctx, b.orgScopeID)
		if err != nil {
			return fmt.Errorf("boundary: list scopes: %w", err)
		}
		id, ok = "", false
		for _, s := range list.Items {
			if s.Name == name {
				id, ok = s.Id, true
				break
			}
		}
		return nil
	})
	return id, ok, err
}

// CatalogIDByName returns the id of the shared host catalog in scopeID, or ok=false.
func (b *Client) CatalogIDByName(ctx context.Context, scopeID string) (string, bool, error) {
	var id string
	var ok bool
	err := b.do(ctx, func() error {
		list, err := hostcatalogs.NewClient(b.c).List(ctx, scopeID)
		if err != nil {
			return fmt.Errorf("boundary: list host catalogs: %w", err)
		}
		id, ok = "", false
		for _, c := range list.Items {
			if c.Name == b.catalogName {
				id, ok = c.Id, true
				break
			}
		}
		return nil
	})
	return id, ok, err
}

// HostSetByName returns the host-set named name in the catalog along with its current
// host ids, or ok=false if the portal has not created it yet.
func (b *Client) HostSetByName(ctx context.Context, catalogID, name string) (string, []string, bool, error) {
	var setID string
	var hostIDs []string
	var ok bool
	err := b.do(ctx, func() error {
		list, err := hostsets.NewClient(b.c).List(ctx, catalogID)
		if err != nil {
			return fmt.Errorf("boundary: list host sets: %w", err)
		}
		setID, hostIDs, ok = "", nil, false
		for _, hs := range list.Items {
			if hs.Name == name {
				// List items omit HostIds — only a Read populates the set's membership.
				full, err := hostsets.NewClient(b.c).Read(ctx, hs.Id)
				if err != nil {
					return fmt.Errorf("boundary: read host set %q: %w", hs.Id, err)
				}
				setID, hostIDs, ok = full.Item.Id, full.Item.HostIds, true
				break
			}
		}
		return nil
	})
	return setID, hostIDs, ok, err
}

// HostAddress reads the static address of a host.
func (b *Client) HostAddress(ctx context.Context, hostID string) (string, error) {
	var addr string
	err := b.do(ctx, func() error {
		h, err := hosts.NewClient(b.c).Read(ctx, hostID)
		if err != nil {
			return fmt.Errorf("boundary: read host %q: %w", hostID, err)
		}
		addr, _ = h.Item.Attributes["address"].(string)
		return nil
	})
	return addr, err
}

// CreateHostInSet creates a static host with the given address and adds it to the set.
// The two API calls are wrapped separately so a re-auth retry never re-runs an
// already-succeeded Create (which would collide on the unique host name).
func (b *Client) CreateHostInSet(ctx context.Context, catalogID, hostSetID, name, address string) error {
	var hostID string
	if err := b.do(ctx, func() error {
		h, err := hosts.NewClient(b.c).Create(ctx, catalogID,
			hosts.WithName(name), hosts.WithStaticHostAddress(address))
		if err != nil {
			return fmt.Errorf("boundary: create host %q: %w", name, err)
		}
		hostID = h.Item.Id
		return nil
	}); err != nil {
		return err
	}
	return b.do(ctx, func() error {
		if _, err := hostsets.NewClient(b.c).AddHosts(ctx, hostSetID, 0, []string{hostID},
			hostsets.WithAutomaticVersioning(true)); err != nil {
			return fmt.Errorf("boundary: add host to set: %w", err)
		}
		return nil
	})
}

// UpdateHostAddress updates a host's static address in place (the host id, and so the
// host-set membership and the target, are unchanged — the target follows the workspace).
func (b *Client) UpdateHostAddress(ctx context.Context, hostID, address string) error {
	return b.do(ctx, func() error {
		if _, err := hosts.NewClient(b.c).Update(ctx, hostID, 0,
			hosts.WithStaticHostAddress(address), hosts.WithAutomaticVersioning(true)); err != nil {
			return fmt.Errorf("boundary: update host %q address: %w", hostID, err)
		}
		return nil
	})
}
