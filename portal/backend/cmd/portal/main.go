// Command portal is the Developer Portal backend: IBM Verify OIDC login plus a
// JSON API that lists projects and creates/lists workspaces by calling Nomad,
// Boundary, and Vault directly. PoC; run locally (see portal/README.md).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/api"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/config"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/workspace"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("portal: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	secureCookies := strings.HasPrefix(cfg.OIDCRedirectURL, "https://")
	authn, err := auth.New(ctx, cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, cfg.OIDCRedirectURL, cfg.SessionSecret, secureCookies)
	if err != nil {
		return err
	}

	vault, err := hashistack.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultKVMount)
	if err != nil {
		return err
	}
	nomad, err := hashistack.NewNomad(cfg.NomadAddr, cfg.NomadToken)
	if err != nil {
		return err
	}
	bndry, err := hashistack.NewBoundary(ctx, cfg.BoundaryAddr, cfg.BoundaryAuthMethodID, cfg.BoundaryLogin, cfg.BoundaryPassword)
	if err != nil {
		return err
	}

	svc := workspace.New(workspace.Config{
		BoundaryAddr:  cfg.BoundaryAddr,
		PortRange:     cfg.PortRange,
		SSHConfigPath: cfg.SSHConfigPath,
	}, vault, nomad, bndry)

	staticDir := os.Getenv("PORTAL_STATIC_DIR")
	if staticDir == "" {
		staticDir = "./web"
	}
	mux := api.NewMux(authn, svc, staticDir)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("portal listening on %s (static: %s)", cfg.ListenAddr, staticDir)
	return srv.ListenAndServe()
}
