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
	BoundaryAddr         string // PORTAL_BOUNDARY_ADDR
	BoundaryAuthMethodID string // PORTAL_BOUNDARY_AUTH_METHOD_ID (admin password method)
	BoundaryLogin        string // PORTAL_BOUNDARY_LOGIN
	BoundaryPassword     string // PORTAL_BOUNDARY_PASSWORD

	// Nomad / Vault.
	NomadAddr    string // PORTAL_NOMAD_ADDR
	NomadToken   string // PORTAL_NOMAD_TOKEN
	VaultAddr    string // PORTAL_VAULT_ADDR
	VaultToken   string // PORTAL_VAULT_TOKEN
	VaultKVMount string // PORTAL_VAULT_KV_MOUNT (default "secret")

	PortRange portgen.Range // PORTAL_SSH_PORT_RANGE = "min-max" (default 2222-2399)

	// SSHConfigPath is the local SSH config the portal writes per-workspace
	// "Open in IDE" blocks into. PORTAL_SSH_CONFIG_PATH; defaults to
	// ~/.ssh/config. Set it to empty to disable the feature (production mode,
	// where the backend is not on the developer's machine).
	SSHConfigPath string
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
		BoundaryAuthMethodID: os.Getenv("PORTAL_BOUNDARY_AUTH_METHOD_ID"),
		BoundaryLogin:        os.Getenv("PORTAL_BOUNDARY_LOGIN"),
		BoundaryPassword:     os.Getenv("PORTAL_BOUNDARY_PASSWORD"),
		NomadAddr:            os.Getenv("PORTAL_NOMAD_ADDR"),
		NomadToken:           os.Getenv("PORTAL_NOMAD_TOKEN"),
		VaultAddr:            os.Getenv("PORTAL_VAULT_ADDR"),
		VaultToken:           os.Getenv("PORTAL_VAULT_TOKEN"),
		VaultKVMount:         env("PORTAL_VAULT_KV_MOUNT", "secret"),
		PortRange:            portgen.DefaultRange,
	}

	if raw := os.Getenv("PORTAL_SSH_PORT_RANGE"); raw != "" {
		r, err := parseRange(raw)
		if err != nil {
			return Config{}, err
		}
		c.PortRange = r
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
