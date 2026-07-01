// Command portal is the Developer Portal backend: IBM Verify OIDC login plus a
// JSON API that lists projects and creates/lists workspaces by calling Nomad,
// Boundary, and Vault directly. It runs locally for the PoC and as a Vault-WIF
// Nomad job in production (see portal/README.md and terraform/infra/developer-portal.tf).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/admin"
	"github.com/secured-dev-workspace/developer-portal/internal/api"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/basetmpladmin"
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/config"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/jobtemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/logging"
	"github.com/secured-dev-workspace/developer-portal/internal/mcpgw"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
	"github.com/secured-dev-workspace/developer-portal/internal/projectadmin"
	"github.com/secured-dev-workspace/developer-portal/internal/projectbootstrap"
	"github.com/secured-dev-workspace/developer-portal/internal/projectrole"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
	"github.com/secured-dev-workspace/developer-portal/internal/workspace"
)

func main() {
	// Structured logging from the very first line so even config errors are JSON;
	// run() re-installs the logger once the configured level is known.
	slog.SetDefault(logging.New(os.Getenv("PORTAL_LOG_LEVEL")))

	// One-shot maintenance subcommands (not the server).
	if len(os.Args) > 1 && os.Args[1] == "backfill-from-vault" {
		if err := runBackfill(); err != nil {
			slog.Error("backfill exited", "err", err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		slog.Error("portal exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := logging.New(cfg.LogLevel)
	slog.SetDefault(logger)

	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	authn, err := auth.New(startCtx, cfg.OIDCIssuer, cfg.OIDCClientID, cfg.OIDCClientSecret, cfg.OIDCRedirectURL, cfg.SessionSecret, cfg.SecureCookies)
	if err != nil {
		return err
	}

	tls := hashistack.TLSOptions{CACertPath: cfg.HashiCACertPath, SkipVerify: cfg.TLSSkipVerify}
	vault, err := hashistack.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultTokenFile, cfg.VaultKVMount, tls)
	if err != nil {
		return err
	}
	nomad, err := hashistack.NewNomad(cfg.NomadAddr, cfg.NomadToken, tls)
	if err != nil {
		return err
	}
	bndry, err := hashistack.NewBoundary(startCtx, cfg.BoundaryAddr, cfg.BoundaryAuthMethodID, cfg.BoundaryLogin, cfg.BoundaryPassword, tls)
	if err != nil {
		return err
	}

	st, err := buildStore(startCtx, cfg)
	if err != nil {
		return err
	}
	if err := jobtemplate.SeedBaseTemplates(startCtx, st); err != nil {
		return err
	}
	projectRoles := projectrole.New(st)

	svc := workspace.New(workspace.Config{
		BoundaryPublicAddr: cfg.BoundaryPublicAddr,
		PortRange:          cfg.PortRange,
		SSHConfigPath:      cfg.SSHConfigPath,
	}, st, nomad, bndry)

	staticDir := os.Getenv("PORTAL_STATIC_DIR")
	if staticDir == "" {
		staticDir = "./web"
	}

	// Platform Admin onboarding plane — additive and optional. Enabled only when
	// the MCP/LLM gateway addresses are configured; its admin credentials are read
	// from Vault at startup so no secret material lives in the portal's env.
	var adminHandlers *admin.Handlers
	var projectMCP *projectadmin.Handlers
	if cfg.AdminEnabled() {
		adminHandlers, projectMCP, err = buildAdminPlane(startCtx, cfg, st, svc, vault, nomad)
		if err != nil {
			return err
		}
		logger.Info("platform-admin onboarding plane enabled", "mcp_gateway", cfg.MCPGatewayAddr, "llm_gateway", cfg.LLMGatewayAddr)
	} else {
		logger.Info("platform-admin onboarding plane disabled (set PORTAL_MCP_GATEWAY_ADDR and PORTAL_LLM_GATEWAY_ADDR to enable)")
	}

	// Project-create plane (platform-admin) — additive and optional. Enabled when
	// the creator broker is configured (a third Nomad workload identity + the
	// root-ns project-creator role, applied by terraform/infra). Independent of the
	// MCP/LLM onboarding plane.
	var projectCreate *projectbootstrap.Handlers
	if cfg.CreatorJWTPath != "" {
		vc := projectbootstrap.NewVaultCreator(vault.APIClient(),
			projectbootstrap.CreatorConfig{JWTPath: cfg.CreatorJWTPath, Role: cfg.CreatorRole, AuthMount: cfg.CreatorAuthMount},
			projectbootstrap.JWKSConfig{URL: cfg.NomadJWKSURL, CAPEM: cfg.NomadCAPEM})
		// The descriptor is persisted to the Postgres control-plane store (st), not
		// Vault KV — portal control-plane state lives in Postgres.
		pbSvc := projectbootstrap.NewService(vc, nomad, bndry, st, projectRoles, st, projectbootstrap.Config{
			NomadOIDCAuthMethod:      cfg.NomadOIDCAuthMethodName,
			BoundaryOrgScopeID:       cfg.BoundaryOrgScopeID,
			BoundaryOIDCAuthMethodID: cfg.BoundaryOIDCAuthMethodID,
			InstancePrivateIP:        cfg.InstancePrivateIP,
		})
		projectCreate = projectbootstrap.NewHandlers(pbSvc)
		logger.Info("project-create plane enabled")
	} else {
		logger.Info("project-create plane disabled (set PORTAL_CREATOR_JWT_PATH to enable)")
	}

	mux := api.NewMux(api.Options{
		Auth:          authn,
		Svc:           svc,
		Admin:         adminHandlers,
		BaseTmpl:      basetmpladmin.NewHandlers(basetmpladmin.New(st)),
		ProjectCreate: projectCreate,
		ProjectRoles:  projectRoles,
		ProjectMCP:    projectMCP,
		Store:         st,
		StaticDir:     staticDir,
		Ready:         newReadinessCheck(vault, nomad),
		RateLimit:     middleware.RateLimitConfig{RPS: cfg.RateLimitRPS, Burst: cfg.RateLimitBurst},
	})

	// Cross-cutting middleware, outermost first: recover → request context (id) →
	// access log → security headers → CSRF → routes.
	serveTLS := cfg.TLSCertFile != "" && cfg.TLSKeyFile != ""
	handler := middleware.Recover(logger)(
		middleware.Context(
			middleware.Logger(logger)(
				middleware.SecurityHeaders(serveTLS)(
					middleware.CSRF(mux)))))

	if cfg.SecureCookies && !serveTLS {
		logger.Warn("secure cookies enabled but the portal is not terminating TLS; terminate TLS upstream or set PORTAL_TLS_CERT_FILE/KEY_FILE")
	}
	if !cfg.SecureCookies {
		logger.Warn("secure cookies disabled (local/PoC); do not expose this deployment without TLS")
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second, // workspace create can wait on GPU placement (~30s) + Boundary calls
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("portal listening", "addr", cfg.ListenAddr, "tls", serveTLS, "static", staticDir)
		if serveTLS {
			errCh <- srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining in-flight requests")
		shutCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// buildStore opens the durable control-plane store when a DSN is configured
// (portal-postgres), otherwise the in-memory store. Both satisfy store.Store and
// back both the admin plane and the project-role plane.
func buildStore(ctx context.Context, cfg config.Config) (store.Store, error) {
	if cfg.DBDSN == "" {
		slog.Info("control-plane store: in-memory (set PORTAL_DB_DSN for durability)")
		return store.NewMemory(), nil
	}
	pg, err := store.NewPostgres(ctx, cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("connect control-plane store: %w", err)
	}
	slog.Info("control-plane store: postgres")
	return pg, nil
}

// buildAdminPlane wires the Platform Admin onboarding service: it reads the MCP
// gateway admin JWT secret/email and the LiteLLM portal-admin key from Vault (the
// portal never holds them in env), constructs the gateway/LLM clients over the
// shared control-plane store, and returns the HTTP handlers.
func buildAdminPlane(ctx context.Context, cfg config.Config, st store.Store, wsvc *workspace.Service, vault *hashistack.Vault, nomad *hashistack.Nomad) (*admin.Handlers, *projectadmin.Handlers, error) {
	jwtSecret, err := vault.ReadKVField(ctx, "infra/mcp-gateway", "jwt_secret_key")
	if err != nil {
		return nil, nil, fmt.Errorf("admin plane: read mcp-gateway jwt secret: %w", err)
	}
	adminEmail, err := vault.ReadKVField(ctx, "infra/mcp-gateway", "admin_email")
	if err != nil {
		return nil, nil, fmt.Errorf("admin plane: read mcp-gateway admin email: %w", err)
	}
	llmKey, err := vault.ReadKVField(ctx, "infra/llm-gateway", "portal_admin_key")
	if err != nil {
		return nil, nil, fmt.Errorf("admin plane: read llm-gateway portal-admin key: %w", err)
	}

	gateway := mcpgw.New(cfg.MCPGatewayAddr, adminEmail, jwtSecret, nil)
	llm := llmgw.New(cfg.LLMGatewayAddr, llmKey, nil)

	vadmin := blueprint.NewVaultAdmin(vault.APIClient(), blueprint.ProvisionerConfig{
		JWTPath:   cfg.ProvisionerJWTPath,
		Role:      cfg.ProvisionerRole,
		AuthMount: cfg.ProvisionerAuthMount,
	})
	executor := blueprint.NewExecutor(vadmin, blueprint.ExecutorConfig{
		AuthPath:      "jwt-nomad",
		BoundAudience: "vault",
		KVMount:       cfg.VaultKVMount,
	})
	validator := blueprint.NewValidator(executor)

	adminSvc := admin.New(st, nomad, gateway, llm, vault, validator, admin.Config{
		MCPNamespace:    cfg.MCPNamespace,
		NodePool:        cfg.AgentNodePool,
		MCPJobVaultRole: cfg.MCPJobVaultRole,
	})
	pmSvc := projectadmin.New(st, wsvc, executor, nomad, gateway, projectadmin.Config{
		NodePool: cfg.AgentNodePool,
	})
	return admin.NewHandlers(adminSvc), projectadmin.NewHandlers(pmSvc), nil
}

// newReadinessCheck returns a /readyz probe that pings Vault and Nomad, caching
// the result briefly so health polling can't hammer the dependencies. Boundary is
// authenticated once at startup; its reachability is not re-probed here.
func newReadinessCheck(v *hashistack.Vault, n *hashistack.Nomad) func(context.Context) error {
	const ttl = 3 * time.Second
	var (
		mu   sync.Mutex
		exp  time.Time
		last error
	)
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(exp) {
			return last
		}
		last = nil
		if err := v.Ping(ctx); err != nil {
			last = err
		} else if err := n.Ping(ctx); err != nil {
			last = err
		}
		exp = time.Now().Add(ttl)
		return last
	}
}
