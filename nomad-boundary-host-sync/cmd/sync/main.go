// Command sync is the Nomad→Boundary host-address reconciler. It keeps each
// workspace's Boundary host address pointed at whatever Nomad node its alloc runs on,
// so the (stable) Boundary target follows the workspace across reschedules.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/boundary"
	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/config"
	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/nomad"
	"github.com/secured-dev-workspace/nomad-boundary-host-sync/internal/reconcile"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	nc, err := nomad.New(cfg.NomadAddr, cfg.NomadToken, cfg.CACertPath, cfg.TLSSkipVerify)
	if err != nil {
		log.Error("nomad client", "err", err)
		os.Exit(1)
	}
	bc, err := boundary.New(ctx, cfg.BoundaryAddr, cfg.BoundaryAuthMethodID, cfg.BoundaryLogin,
		cfg.BoundaryPassword, cfg.BoundaryOrgScopeID, cfg.CatalogName, cfg.CACertPath, cfg.TLSSkipVerify)
	if err != nil {
		log.Error("boundary client", "err", err)
		os.Exit(1)
	}

	log.Info("nomad-boundary-host-sync starting",
		"interval", cfg.ReconcileEvery.String(), "catalog", cfg.CatalogName)
	reconcile.New(nc, bc, log).Run(ctx, cfg.ReconcileEvery)
	log.Info("shutting down")
}
