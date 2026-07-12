// Package reconcile is the level-triggered control loop: every interval it makes each
// live workspace's Boundary host address equal its current Nomad node address. It owns
// no structure — it never creates scopes/catalogs/host-sets/targets and never deletes
// anything (the portal owns that at project/workspace lifecycle). It only ensures the
// single host inside each portal-created host-set, so a rescheduled workspace's stable
// target follows it to the new node.
package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/nomad"
)

// NomadClient lists the current workspace service registrations.
type NomadClient interface {
	ListWorkspaceServices(ctx context.Context) ([]nomad.WorkspaceService, error)
}

// BoundaryClient is the minimal Boundary surface the loop drives. Lookups return
// ok=false (not an error) when a resource is absent, so the loop can skip cleanly.
type BoundaryClient interface {
	ScopeIDByName(ctx context.Context, name string) (string, bool, error)
	CatalogIDByName(ctx context.Context, scopeID string) (string, bool, error)
	HostSetByName(ctx context.Context, catalogID, name string) (string, []string, bool, error)
	HostAddress(ctx context.Context, hostID string) (string, error)
	CreateHostInSet(ctx context.Context, catalogID, hostSetID, name, address string) error
	UpdateHostAddress(ctx context.Context, hostID, address string) error
}

// Reconciler wires a Nomad reader to a Boundary writer.
type Reconciler struct {
	nomad NomadClient
	bnd   BoundaryClient
	log   *slog.Logger
}

// New constructs a Reconciler.
func New(n NomadClient, b BoundaryClient, log *slog.Logger) *Reconciler {
	return &Reconciler{nomad: n, bnd: b, log: log}
}

// Run reconciles once immediately, then every interval until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	r.ReconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.ReconcileOnce(ctx)
		}
	}
}

// ReconcileOnce runs a single pass. Per-service best-effort: one service's failure is
// logged and does not abort the others.
func (r *Reconciler) ReconcileOnce(ctx context.Context) {
	svcs, err := r.nomad.ListWorkspaceServices(ctx)
	if err != nil {
		r.log.Error("list workspace services", "err", err)
		return
	}
	for _, s := range svcs {
		if err := r.ensure(ctx, s); err != nil {
			r.log.Error("ensure host address", "service", s.Name, "project", s.Project, "err", err)
		}
	}
}

// ensure converges one workspace's Boundary host to its current node address.
func (r *Reconciler) ensure(ctx context.Context, s nomad.WorkspaceService) error {
	if s.Project == "" || s.Address == "" {
		r.log.Warn("skipping service with missing project tag or address", "service", s.Name)
		return nil
	}
	scopeID, ok, err := r.bnd.ScopeIDByName(ctx, s.Project)
	if err != nil {
		return err
	}
	if !ok {
		r.log.Warn("no Boundary scope for project", "project", s.Project, "service", s.Name)
		return nil
	}
	catalogID, ok, err := r.bnd.CatalogIDByName(ctx, scopeID)
	if err != nil {
		return err
	}
	if !ok {
		r.log.Warn("no shared host catalog in project scope", "project", s.Project)
		return nil
	}
	setID, hostIDs, ok, err := r.bnd.HostSetByName(ctx, catalogID, s.Name)
	if err != nil {
		return err
	}
	if !ok {
		// The portal creates the host-set at workspace Provision, but the Nomad service
		// can register first (RegisterJob precedes Provision). Skip; a later cycle converges.
		r.log.Debug("host-set not ready yet, will retry", "service", s.Name)
		return nil
	}
	if len(hostIDs) == 0 {
		r.log.Info("registering workspace host", "service", s.Name, "address", s.Address)
		return r.bnd.CreateHostInSet(ctx, catalogID, setID, s.Name, s.Address)
	}
	cur, err := r.bnd.HostAddress(ctx, hostIDs[0])
	if err != nil {
		return err
	}
	if cur == s.Address {
		return nil
	}
	r.log.Info("updating workspace host address", "service", s.Name, "from", cur, "to", s.Address)
	return r.bnd.UpdateHostAddress(ctx, hostIDs[0], s.Address)
}
