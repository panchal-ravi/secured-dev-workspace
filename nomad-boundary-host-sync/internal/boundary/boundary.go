// Package boundary is a thin Boundary API client for the host-sync: it resolves a
// project scope + its shared host catalog + a workspace's host-set by name, and
// creates/updates the single static host inside that set. All structural objects
// (scope, catalog, host-set, target) are owned by the portal; this client only
// manages the host address.
package boundary

import (
	"context"
	"fmt"

	bapi "github.com/hashicorp/boundary/api"
	"github.com/hashicorp/boundary/api/authmethods"
	"github.com/hashicorp/boundary/api/hostcatalogs"
	"github.com/hashicorp/boundary/api/hosts"
	"github.com/hashicorp/boundary/api/hostsets"
	"github.com/hashicorp/boundary/api/scopes"
)

// Client wraps the Boundary controller API, authenticated as the admin password
// account (same as the portal).
type Client struct {
	c           *bapi.Client
	orgScopeID  string
	catalogName string
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
	return &Client{c: c, orgScopeID: orgScopeID, catalogName: catalogName}, nil
}

// ScopeIDByName returns the id of the project scope named name (a direct child of the
// org scope), or ok=false if absent.
func (b *Client) ScopeIDByName(ctx context.Context, name string) (string, bool, error) {
	list, err := scopes.NewClient(b.c).List(ctx, b.orgScopeID)
	if err != nil {
		return "", false, fmt.Errorf("boundary: list scopes: %w", err)
	}
	for _, s := range list.Items {
		if s.Name == name {
			return s.Id, true, nil
		}
	}
	return "", false, nil
}

// CatalogIDByName returns the id of the shared host catalog in scopeID, or ok=false.
func (b *Client) CatalogIDByName(ctx context.Context, scopeID string) (string, bool, error) {
	list, err := hostcatalogs.NewClient(b.c).List(ctx, scopeID)
	if err != nil {
		return "", false, fmt.Errorf("boundary: list host catalogs: %w", err)
	}
	for _, c := range list.Items {
		if c.Name == b.catalogName {
			return c.Id, true, nil
		}
	}
	return "", false, nil
}

// HostSetByName returns the host-set named name in the catalog along with its current
// host ids, or ok=false if the portal has not created it yet.
func (b *Client) HostSetByName(ctx context.Context, catalogID, name string) (string, []string, bool, error) {
	list, err := hostsets.NewClient(b.c).List(ctx, catalogID)
	if err != nil {
		return "", nil, false, fmt.Errorf("boundary: list host sets: %w", err)
	}
	for _, hs := range list.Items {
		if hs.Name == name {
			// List items omit HostIds — only a Read populates the set's membership.
			full, err := hostsets.NewClient(b.c).Read(ctx, hs.Id)
			if err != nil {
				return "", nil, false, fmt.Errorf("boundary: read host set %q: %w", hs.Id, err)
			}
			return full.Item.Id, full.Item.HostIds, true, nil
		}
	}
	return "", nil, false, nil
}

// HostAddress reads the static address of a host.
func (b *Client) HostAddress(ctx context.Context, hostID string) (string, error) {
	h, err := hosts.NewClient(b.c).Read(ctx, hostID)
	if err != nil {
		return "", fmt.Errorf("boundary: read host %q: %w", hostID, err)
	}
	addr, _ := h.Item.Attributes["address"].(string)
	return addr, nil
}

// CreateHostInSet creates a static host with the given address and adds it to the set.
func (b *Client) CreateHostInSet(ctx context.Context, catalogID, hostSetID, name, address string) error {
	h, err := hosts.NewClient(b.c).Create(ctx, catalogID,
		hosts.WithName(name), hosts.WithStaticHostAddress(address))
	if err != nil {
		return fmt.Errorf("boundary: create host %q: %w", name, err)
	}
	if _, err := hostsets.NewClient(b.c).AddHosts(ctx, hostSetID, 0, []string{h.Item.Id},
		hostsets.WithAutomaticVersioning(true)); err != nil {
		return fmt.Errorf("boundary: add host to set: %w", err)
	}
	return nil
}

// UpdateHostAddress updates a host's static address in place (the host id, and so the
// host-set membership and the target, are unchanged — the target follows the workspace).
func (b *Client) UpdateHostAddress(ctx context.Context, hostID, address string) error {
	if _, err := hosts.NewClient(b.c).Update(ctx, hostID, 0,
		hosts.WithStaticHostAddress(address), hosts.WithAutomaticVersioning(true)); err != nil {
		return fmt.Errorf("boundary: update host %q address: %w", hostID, err)
	}
	return nil
}
