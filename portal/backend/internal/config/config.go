// Package config loads the portal's runtime configuration from the environment.
//
// For the PoC the privileged HashiStack credentials are injected as env vars
// (populated from `terraform output`); production replaces these with Vault WIF
// (see the plan's roadmap). Every value is required unless a default is noted.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/portgen"
)

type Config struct {
	ListenAddr string // PORTAL_LISTEN_ADDR (default ":8080")

	// OIDC (the portal's own IBM Verify app).
	OIDCIssuer       string // PORTAL_OIDC_ISSUER
	OIDCClientID     string // PORTAL_OIDC_CLIENT_ID
	OIDCClientSecret string // PORTAL_OIDC_CLIENT_SECRET
	OIDCRedirectURL  string // PORTAL_OIDC_REDIRECT_URL
	SessionSecret    string // PORTAL_SESSION_SECRET (cookie signing key)

	// Boundary (admin password auth for provisioning).
	BoundaryAddr         string // PORTAL_BOUNDARY_ADDR (server-side API client; loopback on the node)
	BoundaryPublicAddr   string // PORTAL_BOUNDARY_PUBLIC_ADDR (externally reachable NLB addr for the developer-facing auth/connect commands; defaults to BoundaryAddr)
	BoundaryAuthMethodID string // PORTAL_BOUNDARY_AUTH_METHOD_ID (admin password method)
	BoundaryLogin        string // PORTAL_BOUNDARY_LOGIN
	BoundaryPassword     string // PORTAL_BOUNDARY_PASSWORD

	// Nomad / Vault.
	NomadAddr      string // PORTAL_NOMAD_ADDR
	NomadToken     string // PORTAL_NOMAD_TOKEN
	VaultAddr      string // PORTAL_VAULT_ADDR
	VaultToken     string // PORTAL_VAULT_TOKEN (or read from VaultTokenFile)
	VaultTokenFile string // PORTAL_VAULT_TOKEN_FILE — WIF: Nomad-managed, re-read on rotation
	VaultKVMount   string // PORTAL_VAULT_KV_MOUNT (default "secret")

	PortRange portgen.Range // PORTAL_SSH_PORT_RANGE = "min-max" (default 2222-2399)

	// SSHConfigPath is the local SSH config the portal writes per-workspace
	// "Open in IDE" blocks into. PORTAL_SSH_CONFIG_PATH; defaults to
	// ~/.ssh/config. Set it to empty to disable the feature (production mode,
	// where the backend is not on the developer's machine).
	SSHConfigPath string

	// Operational hardening (all optional; PoC-safe defaults).
	LogLevel        string        // PORTAL_LOG_LEVEL (debug|info|warn|error, default info)
	SecureCookies   bool          // PORTAL_SECURE_COOKIES (default: inferred from the redirect-URL scheme)
	ShutdownTimeout time.Duration // PORTAL_SHUTDOWN_TIMEOUT seconds (default 15s)

	// Server-side TLS termination. When both are set the portal serves HTTPS
	// (production: :8443 so Secure cookies are honored); unset = plain HTTP (local).
	TLSCertFile string // PORTAL_TLS_CERT_FILE
	TLSKeyFile  string // PORTAL_TLS_KEY_FILE

	// HashiStack client TLS. The stack is self-signed: SkipVerify defaults true
	// (PoC), production sets it false and supplies the CA path so connections are
	// actually authenticated.
	HashiCACertPath string // PORTAL_HASHISTACK_CA_CERT (PEM path)
	TLSSkipVerify   bool   // PORTAL_TLS_SKIP_VERIFY (default true)

	// Per-user rate limit on mutating endpoints. RPS<=0 disables.
	RateLimitRPS   float64 // PORTAL_RATE_LIMIT_RPS (default 5)
	RateLimitBurst int     // PORTAL_RATE_LIMIT_BURST (default 10)

	// Platform Admin onboarding plane (all optional). The plane is enabled only
	// when both gateway addresses are set; the admin JWT secret / admin email /
	// LiteLLM portal-admin key are read from Vault at startup, not from env.
	MCPGatewayAddr  string // PORTAL_MCP_GATEWAY_ADDR (ContextForge admin URL, e.g. http://<node>:4444)
	LLMGatewayAddr  string // PORTAL_LLM_GATEWAY_ADDR (LiteLLM admin URL, e.g. http://<node>:4000)
	MCPNamespace    string // PORTAL_MCP_NAMESPACE (default "infra-mcp")
	AgentNodePool   string // PORTAL_AGENT_NODE_POOL (default "agents")
	MCPJobVaultRole string // PORTAL_MCP_JOB_VAULT_ROLE (WIF role for MCP jobs that reference Vault secrets)
	DBDSN           string // PORTAL_DB_DSN (libpq DSN for the durable control-plane store; in-memory store if unset)

	// Blueprint provisioner (project-deploy plane). The portal brokers a token
	// native to each project namespace via a second Nomad workload identity, then
	// provisions with it — no standing cross-namespace privilege. Empty JWTPath
	// disables the broker (plane off / infra not applied); Instantiate then returns
	// a 400 rather than a raw 403.
	ProvisionerJWTPath   string // PORTAL_PROVISIONER_JWT_PATH (Nomad-written workload-identity JWT file)
	ProvisionerRole      string // PORTAL_PROVISIONER_ROLE (default "portal-provisioner")
	ProvisionerAuthMount string // PORTAL_PROVISIONER_AUTH_MOUNT (default "jwt-nomad")

	// Project-creator broker (project-create plane). A third Nomad workload
	// identity the portal exchanges at the ROOT-namespace jwt-nomad backend for a
	// short-TTL token scoped to create child namespaces + bootstrap their auth.
	// Empty JWTPath disables project creation (plane off / infra not applied).
	CreatorJWTPath   string // PORTAL_CREATOR_JWT_PATH (Nomad-written workload-identity JWT file)
	CreatorRole      string // PORTAL_CREATOR_ROLE (default "project-creator")
	CreatorAuthMount string // PORTAL_CREATOR_AUTH_MOUNT (default "jwt-nomad")

	// Nomad JWKS trust the creator installs into each new project namespace's
	// jwt-nomad backend so project workloads can federate.
	NomadJWKSURL   string // PORTAL_NOMAD_JWKS_URL (default "https://127.0.0.1:4646/.well-known/jwks.json")
	NomadCAPEM     string // PORTAL_NOMAD_CA_PEM (PEM literal; may be empty)
	NomadCAPEMFile string // PORTAL_NOMAD_CA_PEM_FILE (multi-line PEM is delivered as a file)

	// NomadOIDCAuthMethodName is the Nomad OIDC auth method the per-project ACL
	// binding rule attaches to. PORTAL_NOMAD_OIDC_AUTH_METHOD.
	NomadOIDCAuthMethodName string

	// BoundaryOIDCAuthMethodID is recorded in each project descriptor so the
	// workspace connect flow knows which Boundary auth method to use.
	BoundaryOIDCAuthMethodID string // PORTAL_BOUNDARY_OIDC_AUTH_METHOD_ID

	// BoundaryOrgScopeID is the parent org scope under which project scopes are
	// created. PORTAL_BOUNDARY_ORG_SCOPE_ID.
	BoundaryOrgScopeID string

	// InstancePrivateIP is written into each project descriptor (all-in-one node).
	InstancePrivateIP string // PORTAL_INSTANCE_PRIVATE_IP

	// Project engine-provisioning (Phase C). Values the portal bakes into project
	// templates + uses when standing up per-project engines from the Portal.
	LLMGatewayPrivateEndpoint string // PORTAL_LLM_GATEWAY_PRIVATE_ENDPOINT (node-ip:4000 — what a workspace uses as llm_base_url)
	VaultCredStoreAddress     string // PORTAL_VAULT_CRED_STORE_ADDR (Vault addr Boundary reaches Vault at; defaults to VaultAddr)
	GithubPluginVersion       string // PORTAL_GITHUB_PLUGIN_VERSION (default "2.3.2"; mounted as v<version>)

	// Governed LLM models, defined once in terraform/infra (the LiteLLM model_list)
	// and threaded here so the per-project virtual key's allowed set and the base
	// templates' Claude Code model mapping reference the same names.
	LLMModels       []string // PORTAL_LLM_MODELS (comma-separated; allowed models on a project virtual key)
	LLMModelPrimary string   // PORTAL_LLM_MODEL_PRIMARY (opus/sonnet slot; baked as llm_model_primary)
	LLMModelFast    string   // PORTAL_LLM_MODEL_FAST (haiku/subagent slot; baked as llm_model_fast)
}

// AdminEnabled reports whether the Platform Admin onboarding plane is configured.
func (c Config) AdminEnabled() bool {
	return c.MCPGatewayAddr != "" && c.LLMGatewayAddr != ""
}

// Load reads the configuration from the environment, returning an error that
// lists every missing required variable at once.
func Load() (Config, error) {
	c := Config{
		ListenAddr:           env("PORTAL_LISTEN_ADDR", ":8080"),
		OIDCIssuer:           os.Getenv("PORTAL_OIDC_ISSUER"),
		OIDCClientID:         os.Getenv("PORTAL_OIDC_CLIENT_ID"),
		OIDCClientSecret:     os.Getenv("PORTAL_OIDC_CLIENT_SECRET"),
		OIDCRedirectURL:      env("PORTAL_OIDC_REDIRECT_URL", "http://localhost:8080/auth/callback"),
		SessionSecret:        os.Getenv("PORTAL_SESSION_SECRET"),
		BoundaryAddr:         os.Getenv("PORTAL_BOUNDARY_ADDR"),
		BoundaryPublicAddr:   os.Getenv("PORTAL_BOUNDARY_PUBLIC_ADDR"),
		BoundaryAuthMethodID: os.Getenv("PORTAL_BOUNDARY_AUTH_METHOD_ID"),
		BoundaryLogin:        os.Getenv("PORTAL_BOUNDARY_LOGIN"),
		BoundaryPassword:     os.Getenv("PORTAL_BOUNDARY_PASSWORD"),
		NomadAddr:            os.Getenv("PORTAL_NOMAD_ADDR"),
		NomadToken:           os.Getenv("PORTAL_NOMAD_TOKEN"),
		VaultAddr:            os.Getenv("PORTAL_VAULT_ADDR"),
		VaultToken:           os.Getenv("PORTAL_VAULT_TOKEN"),
		VaultTokenFile:       os.Getenv("PORTAL_VAULT_TOKEN_FILE"),
		VaultKVMount:         env("PORTAL_VAULT_KV_MOUNT", "secret"),
		PortRange:            portgen.DefaultRange,
		LogLevel:             env("PORTAL_LOG_LEVEL", "info"),
		ShutdownTimeout:      time.Duration(envInt("PORTAL_SHUTDOWN_TIMEOUT", 15)) * time.Second,
		TLSCertFile:          os.Getenv("PORTAL_TLS_CERT_FILE"),
		TLSKeyFile:           os.Getenv("PORTAL_TLS_KEY_FILE"),
		HashiCACertPath:      os.Getenv("PORTAL_HASHISTACK_CA_CERT"),
		TLSSkipVerify:        envBool("PORTAL_TLS_SKIP_VERIFY", true),
		RateLimitRPS:         envFloat("PORTAL_RATE_LIMIT_RPS", 5),
		RateLimitBurst:       envInt("PORTAL_RATE_LIMIT_BURST", 10),
		MCPGatewayAddr:       os.Getenv("PORTAL_MCP_GATEWAY_ADDR"),
		LLMGatewayAddr:       os.Getenv("PORTAL_LLM_GATEWAY_ADDR"),
		MCPNamespace:         env("PORTAL_MCP_NAMESPACE", "infra-mcp"),
		AgentNodePool:        env("PORTAL_AGENT_NODE_POOL", "agents"),
		MCPJobVaultRole:      os.Getenv("PORTAL_MCP_JOB_VAULT_ROLE"),
		DBDSN:                os.Getenv("PORTAL_DB_DSN"),
		ProvisionerJWTPath:   os.Getenv("PORTAL_PROVISIONER_JWT_PATH"),
		ProvisionerRole:      env("PORTAL_PROVISIONER_ROLE", "portal-provisioner"),
		ProvisionerAuthMount: env("PORTAL_PROVISIONER_AUTH_MOUNT", "jwt-nomad"),

		CreatorJWTPath:   os.Getenv("PORTAL_CREATOR_JWT_PATH"),
		CreatorRole:      env("PORTAL_CREATOR_ROLE", "project-creator"),
		CreatorAuthMount: env("PORTAL_CREATOR_AUTH_MOUNT", "jwt-nomad"),

		NomadJWKSURL:            env("PORTAL_NOMAD_JWKS_URL", "https://127.0.0.1:4646/.well-known/jwks.json"),
		NomadCAPEM:              os.Getenv("PORTAL_NOMAD_CA_PEM"),
		NomadCAPEMFile:          os.Getenv("PORTAL_NOMAD_CA_PEM_FILE"),
		NomadOIDCAuthMethodName: os.Getenv("PORTAL_NOMAD_OIDC_AUTH_METHOD"),

		BoundaryOIDCAuthMethodID: os.Getenv("PORTAL_BOUNDARY_OIDC_AUTH_METHOD_ID"),
		BoundaryOrgScopeID:       os.Getenv("PORTAL_BOUNDARY_ORG_SCOPE_ID"),
		InstancePrivateIP:        os.Getenv("PORTAL_INSTANCE_PRIVATE_IP"),

		LLMGatewayPrivateEndpoint: os.Getenv("PORTAL_LLM_GATEWAY_PRIVATE_ENDPOINT"),
		VaultCredStoreAddress:     os.Getenv("PORTAL_VAULT_CRED_STORE_ADDR"),
		GithubPluginVersion:       env("PORTAL_GITHUB_PLUGIN_VERSION", "2.3.2"),

		LLMModels:       splitList(env("PORTAL_LLM_MODELS", "deepseek-v4-pro,deepseek-v4-flash")),
		LLMModelPrimary: env("PORTAL_LLM_MODEL_PRIMARY", "deepseek-v4-pro"),
		LLMModelFast:    env("PORTAL_LLM_MODEL_FAST", "deepseek-v4-flash"),
	}

	// Boundary reaches Vault at the same address the portal does unless overridden.
	if c.VaultCredStoreAddress == "" {
		c.VaultCredStoreAddress = c.VaultAddr
	}

	// Secure cookies: explicit override, else inferred from the redirect scheme.
	c.SecureCookies = envBool("PORTAL_SECURE_COOKIES", strings.HasPrefix(c.OIDCRedirectURL, "https://"))

	// The developer-facing Boundary commands (authenticate/connect) run on the
	// developer's machine, so they need the externally reachable address — not the
	// loopback the on-node portal uses for its own API client. Local-run mode sets
	// only PORTAL_BOUNDARY_ADDR (already the NLB), so it falls through to that.
	if c.BoundaryPublicAddr == "" {
		c.BoundaryPublicAddr = c.BoundaryAddr
	}

	// Nomad JWKS CA is multi-line PEM, so it is delivered as a file the creator
	// broker installs into each new project namespace's jwt-nomad config.
	if c.NomadCAPEM == "" && c.NomadCAPEMFile != "" {
		b, err := os.ReadFile(c.NomadCAPEMFile)
		if err != nil {
			return Config{}, fmt.Errorf("config: read PORTAL_NOMAD_CA_PEM_FILE: %w", err)
		}
		c.NomadCAPEM = string(b)
	}

	// WIF deploy: the Vault token lives in a Nomad-managed file. Seed the initial
	// value from it (the client re-reads on rotation).
	if c.VaultTokenFile != "" {
		b, err := os.ReadFile(c.VaultTokenFile)
		if err != nil {
			return Config{}, fmt.Errorf("config: read PORTAL_VAULT_TOKEN_FILE: %w", err)
		}
		c.VaultToken = strings.TrimSpace(string(b))
	}

	if raw := os.Getenv("PORTAL_SSH_PORT_RANGE"); raw != "" {
		r, err := parseRange(raw)
		if err != nil {
			return Config{}, err
		}
		c.PortRange = r
	}
	if c.PortRange.Min <= 0 || c.PortRange.Min > c.PortRange.Max {
		return Config{}, fmt.Errorf("config: invalid SSH port range %d-%d", c.PortRange.Min, c.PortRange.Max)
	}

	// Default the SSH config path to ~/.ssh/config unless explicitly set (empty
	// string disables). os.LookupEnv distinguishes "unset" from "set to empty".
	if v, set := os.LookupEnv("PORTAL_SSH_CONFIG_PATH"); set {
		c.SSHConfigPath = v
	} else if home, err := os.UserHomeDir(); err == nil {
		c.SSHConfigPath = filepath.Join(home, ".ssh", "config")
	}

	required := map[string]string{
		"PORTAL_OIDC_ISSUER":             c.OIDCIssuer,
		"PORTAL_OIDC_CLIENT_ID":          c.OIDCClientID,
		"PORTAL_OIDC_CLIENT_SECRET":      c.OIDCClientSecret,
		"PORTAL_SESSION_SECRET":          c.SessionSecret,
		"PORTAL_BOUNDARY_ADDR":           c.BoundaryAddr,
		"PORTAL_BOUNDARY_AUTH_METHOD_ID": c.BoundaryAuthMethodID,
		"PORTAL_BOUNDARY_LOGIN":          c.BoundaryLogin,
		"PORTAL_BOUNDARY_PASSWORD":       c.BoundaryPassword,
		"PORTAL_NOMAD_ADDR":              c.NomadAddr,
		"PORTAL_NOMAD_TOKEN":             c.NomadToken,
		"PORTAL_VAULT_ADDR":              c.VaultAddr,
		"PORTAL_VAULT_TOKEN":             c.VaultToken,
	}
	var missing []string
	for k, v := range required {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("config: missing required env vars: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList parses a comma-separated env value into a trimmed, non-empty slice.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

func parseRange(raw string) (portgen.Range, error) {
	parts := strings.SplitN(raw, "-", 2)
	if len(parts) != 2 {
		return portgen.Range{}, fmt.Errorf("config: PORTAL_SSH_PORT_RANGE must be \"min-max\", got %q", raw)
	}
	min, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	max, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return portgen.Range{}, fmt.Errorf("config: PORTAL_SSH_PORT_RANGE must be numeric, got %q", raw)
	}
	return portgen.Range{Min: min, Max: max}, nil
}
