package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/config"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/llmgw"
	"github.com/secured-dev-workspace/developer-portal/internal/projectengines"
	"github.com/secured-dev-workspace/developer-portal/internal/projecttemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// cliLookup is an operator ProjectLookup that reads the descriptor straight from the
// store, bypassing the Verify-group membership check (the CLI is a node-local
// operator tool, not a browser session).
type cliLookup struct{ st store.Store }

func (l cliLookup) GetProject(ctx context.Context, name string, _ []string) (descriptor.Descriptor, error) {
	pd, err := l.st.GetProjectDescriptor(ctx, name)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	return descriptor.Parse(string(pd.Descriptor))
}

// runProvision drives the project-template create + engine provision server-side,
// using the SAME wired dependencies as the HTTP planes (the §5 Vault broker, the
// Boundary admin client, the LiteLLM client). It exists so an operator can run the
// Phase-C flow without a browser OIDC session (the API planes are project-admin
// gated). Inputs come from PROVISION_* env; the GitHub App private key is read from a
// file path (PROVISION_GH_KEY_FILE) and used write-only — it is sent to Vault and
// never logged. Invoke as `portal provision-project`.
func runProvision() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DBDSN == "" {
		return fmt.Errorf("provision requires PORTAL_DB_DSN")
	}
	project := strings.TrimSpace(os.Getenv("PROVISION_PROJECT"))
	if project == "" {
		return fmt.Errorf("PROVISION_PROJECT is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	st, err := store.NewPostgres(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer st.Close()

	tls := hashistack.TLSOptions{CACertPath: cfg.HashiCACertPath, SkipVerify: cfg.TLSSkipVerify}
	vault, err := hashistack.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultTokenFile, cfg.VaultKVMount, tls)
	if err != nil {
		return err
	}
	bndry, err := hashistack.NewBoundary(ctx, cfg.BoundaryAddr, cfg.BoundaryAuthMethodID, cfg.BoundaryLogin, cfg.BoundaryPassword, tls)
	if err != nil {
		return err
	}
	llmKey, err := vault.ReadKVField(ctx, "infra/llm-gateway", "portal_admin_key")
	if err != nil {
		return fmt.Errorf("read llm-gateway portal-admin key: %w", err)
	}
	llm := llmgw.New(cfg.LLMGatewayAddr, llmKey, nil)
	vadmin := blueprint.NewVaultAdmin(vault.APIClient(), blueprint.ProvisionerConfig{
		JWTPath:   cfg.ProvisionerJWTPath,
		Role:      cfg.ProvisionerRole,
		AuthMount: cfg.ProvisionerAuthMount,
	})
	lookup := cliLookup{st: st}

	// 1. Project-template create (optional; skipped if PROVISION_BASE is empty).
	if base := strings.TrimSpace(os.Getenv("PROVISION_BASE")); base != "" {
		pt := projecttemplate.New(st, lookup, st, nil, nil, projecttemplate.Config{
			LLMGatewayPrivateEndpoint: cfg.LLMGatewayPrivateEndpoint,
			LLMModelPrimary:           cfg.LLMModelPrimary,
			LLMModelFast:              cfg.LLMModelFast,
		})
		created, err := pt.Create(ctx, "cli", nil, project, projecttemplate.CreateInput{
			Base:       base,
			Flavor:     strings.TrimSpace(os.Getenv("PROVISION_FLAVOR")),
			GitRepoURL: strings.TrimSpace(os.Getenv("PROVISION_GIT_REPO_URL")),
			NodePool:   strings.TrimSpace(os.Getenv("PROVISION_NODE_POOL")),
		})
		if err != nil {
			return fmt.Errorf("project-template create: %w", err)
		}
		slog.Info("template created", "project", project, "flavor", created.Flavor, "base_version", created.BaseVersion)
	}

	// 2. Engine provision (skipped if PROVISION_GH_KEY_FILE is empty).
	keyFile := strings.TrimSpace(os.Getenv("PROVISION_GH_KEY_FILE"))
	if keyFile == "" {
		slog.Info("skipping engine provision (PROVISION_GH_KEY_FILE unset)")
		return nil
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read github key file: %w", err)
	}
	appID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("PROVISION_GH_APP_ID")))
	if err != nil {
		return fmt.Errorf("PROVISION_GH_APP_ID: %w", err)
	}
	installID, err := strconv.Atoi(strings.TrimSpace(os.Getenv("PROVISION_GH_INSTALL_ID")))
	if err != nil {
		return fmt.Errorf("PROVISION_GH_INSTALL_ID: %w", err)
	}
	var repos []string
	if r := strings.TrimSpace(os.Getenv("PROVISION_GH_REPOS")); r != "" {
		for _, s := range strings.Split(r, ",") {
			if s = strings.TrimSpace(s); s != "" {
				repos = append(repos, s)
			}
		}
	}

	// The CLI provisions engines only (no MCP add-on wiring), so the gateway client is nil.
	pe := projectengines.New(vadmin, bndry, llm, nil, st, lookup, st, projectengines.Config{
		VaultCredStoreAddress:     cfg.VaultCredStoreAddress,
		LLMGatewayPrivateEndpoint: cfg.LLMGatewayPrivateEndpoint,
		GithubPluginVersion:       cfg.GithubPluginVersion,
		LLMModels:                 cfg.LLMModels,
	})
	d, err := pe.Provision(ctx, "cli", nil, project, projectengines.ProvisionInput{
		GithubAppID:             appID,
		GithubAppInstallationID: installID,
		GithubAppPrivateKey:     string(pem),
		GithubRepositories:      repos,
	})
	if err != nil {
		return err
	}
	slog.Info("engines provisioned", "project", project, "credential_library_id", d.CredentialLibraryID)
	return nil
}
