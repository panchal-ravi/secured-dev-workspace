// Package config loads the host-sync service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	NomadAddr  string
	NomadToken string

	BoundaryAddr         string
	BoundaryAuthMethodID string
	BoundaryLogin        string
	BoundaryPassword     string
	BoundaryOrgScopeID   string

	// CatalogName is the fixed per-project host-catalog name the portal creates at
	// project bootstrap (default "dev-workspaces"); the reconciler looks it up by name.
	CatalogName string

	CACertPath    string
	TLSSkipVerify bool

	ReconcileEvery time.Duration
}

// Load reads and validates the environment. TLS verification defaults to skipped
// (the stack uses self-signed loopback certs), matching the portal.
func Load() (Config, error) {
	c := Config{
		NomadAddr:            os.Getenv("SYNC_NOMAD_ADDR"),
		NomadToken:           os.Getenv("SYNC_NOMAD_TOKEN"),
		BoundaryAddr:         os.Getenv("SYNC_BOUNDARY_ADDR"),
		BoundaryAuthMethodID: os.Getenv("SYNC_BOUNDARY_AUTH_METHOD_ID"),
		BoundaryLogin:        os.Getenv("SYNC_BOUNDARY_LOGIN"),
		BoundaryPassword:     os.Getenv("SYNC_BOUNDARY_PASSWORD"),
		BoundaryOrgScopeID:   os.Getenv("SYNC_BOUNDARY_ORG_SCOPE_ID"),
		CatalogName:          envOr("SYNC_CATALOG_NAME", "dev-workspaces"),
		CACertPath:           os.Getenv("SYNC_CA_CERT_PATH"),
		TLSSkipVerify:        os.Getenv("SYNC_TLS_SKIP_VERIFY") != "false",
		ReconcileEvery:       10 * time.Second,
	}
	if v := os.Getenv("SYNC_RECONCILE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("SYNC_RECONCILE_INTERVAL: %w", err)
		}
		c.ReconcileEvery = d
	}
	required := map[string]string{
		"SYNC_NOMAD_ADDR":              c.NomadAddr,
		"SYNC_BOUNDARY_ADDR":           c.BoundaryAddr,
		"SYNC_BOUNDARY_AUTH_METHOD_ID": c.BoundaryAuthMethodID,
		"SYNC_BOUNDARY_LOGIN":          c.BoundaryLogin,
		"SYNC_BOUNDARY_PASSWORD":       c.BoundaryPassword,
		"SYNC_BOUNDARY_ORG_SCOPE_ID":   c.BoundaryOrgScopeID,
	}
	for k, v := range required {
		if v == "" {
			return Config{}, fmt.Errorf("%s is required", k)
		}
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
