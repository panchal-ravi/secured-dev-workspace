// Package nomad reads workspace service registrations from Nomad's native service
// discovery. Workspaces register a `provider = "nomad"` service tagged
// service-type=workspace and project=<namespace> with address_mode=host, so each
// registration carries the workspace's current node IP + static SSH port.
package nomad

import (
	"context"
	"fmt"
	"strings"

	napi "github.com/hashicorp/nomad/api"
)

const (
	workspaceTag = "service-type=workspace"
	projectTag   = "project="
)

// WorkspaceService is a workspace's current registration, reduced to what the
// reconciler needs. Name == the Nomad job name == the Boundary target/host-set name.
type WorkspaceService struct {
	Name    string
	Project string // Nomad namespace / Boundary project scope name (from the project= tag)
	Address string // node IP (address_mode=host)
	Port    int    // static host SSH port
}

// Client wraps the Nomad API for read-only service discovery.
type Client struct{ c *napi.Client }

// New builds a Nomad API client. skipVerify + caCertPath mirror the portal's TLS setup.
func New(addr, token, caCertPath string, skipVerify bool) (*Client, error) {
	cfg := napi.DefaultConfig()
	cfg.Address = addr
	cfg.SecretID = token
	cfg.TLSConfig = &napi.TLSConfig{CACert: caCertPath, Insecure: skipVerify}
	c, err := napi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("nomad: new client: %w", err)
	}
	return &Client{c: c}, nil
}

// ListWorkspaceServices returns the current registration for every workspace service
// across all namespaces (one per service name — the most recent alloc's, so a brief
// reschedule overlap resolves to the new node).
func (c *Client) ListWorkspaceServices(ctx context.Context) ([]WorkspaceService, error) {
	qo := (&napi.QueryOptions{Namespace: "*"}).WithContext(ctx)
	stubs, _, err := c.c.Services().List(qo)
	if err != nil {
		return nil, fmt.Errorf("nomad: list services: %w", err)
	}
	var out []WorkspaceService
	for _, nsStub := range stubs {
		for _, svc := range nsStub.Services {
			if !hasTag(svc.Tags, workspaceTag) {
				continue
			}
			regs, _, err := c.c.Services().Get(svc.ServiceName,
				(&napi.QueryOptions{Namespace: nsStub.Namespace}).WithContext(ctx))
			if err != nil {
				return nil, fmt.Errorf("nomad: get service %q: %w", svc.ServiceName, err)
			}
			reg := pickCurrent(regs)
			if reg == nil {
				continue
			}
			out = append(out, WorkspaceService{
				Name:    reg.ServiceName,
				Project: tagValue(reg.Tags, projectTag),
				Address: reg.Address,
				Port:    reg.Port,
			})
		}
	}
	return out, nil
}

// pickCurrent returns the highest-ModifyIndex registration (the most recent alloc's),
// or nil if there are none.
func pickCurrent(regs []*napi.ServiceRegistration) *napi.ServiceRegistration {
	var best *napi.ServiceRegistration
	for _, r := range regs {
		if best == nil || r.ModifyIndex > best.ModifyIndex {
			best = r
		}
	}
	return best
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// tagValue returns the value of the first tag with the given "key=" prefix, or "".
func tagValue(tags []string, prefix string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, prefix) {
			return strings.TrimPrefix(t, prefix)
		}
	}
	return ""
}
