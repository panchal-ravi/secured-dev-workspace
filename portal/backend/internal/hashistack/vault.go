package hashistack

import (
	"context"
	"fmt"
	"os"
	"strings"

	vapi "github.com/hashicorp/vault/api"
)

// Vault wraps the KV-v2 secret reads/writes the portal still needs from Vault
// (single-field reads for the admin plane's provider keys, and the MCP-publish
// descriptor write). Project descriptors and job templates moved to the Postgres
// control-plane store; Vault holds only secrets now.
type Vault struct {
	c         *vapi.Client
	mount     string
	tokenFile string // when set (WIF deploy), re-read before each call so a rotated token is picked up
}

// NewVault builds the client. token is the initial token; tokenFile (optional)
// points at a file Nomad keeps refreshed for WIF — when set it takes precedence
// and is re-read before each operation. tls controls certificate verification.
func NewVault(addr, token, tokenFile, mount string, tls TLSOptions) (*Vault, error) {
	cfg := vapi.DefaultConfig()
	cfg.Address = addr
	if err := cfg.ConfigureTLS(&vapi.TLSConfig{CACert: tls.CACertPath, Insecure: tls.SkipVerify}); err != nil {
		return nil, fmt.Errorf("vault: configure tls: %w", err)
	}
	c, err := vapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}
	c.SetToken(token)
	v := &Vault{c: c, mount: mount, tokenFile: tokenFile}
	if err := v.refreshToken(); err != nil {
		return nil, err
	}
	return v, nil
}

// refreshToken re-reads the token file (WIF) and updates the client. No-op when
// no file is configured (static-token / local PoC mode).
func (v *Vault) refreshToken() error {
	if v.tokenFile == "" {
		return nil
	}
	b, err := os.ReadFile(v.tokenFile)
	if err != nil {
		return fmt.Errorf("vault: read token file: %w", err)
	}
	v.c.SetToken(strings.TrimSpace(string(b)))
	return nil
}

// APIClient exposes the underlying Vault API client for privileged admin clients
// (the blueprint engine) that need engine-lifecycle operations beyond KV.
func (v *Vault) APIClient() *vapi.Client { return v.c }

// Ping checks Vault is reachable (unauthenticated health endpoint), for readiness.
func (v *Vault) Ping(ctx context.Context) error {
	if _, err := v.c.Sys().HealthWithContext(ctx); err != nil {
		return fmt.Errorf("vault: health: %w", err)
	}
	return nil
}

// ReadKVField reads one string field from a KV-v2 secret at relPath (under the
// mount). Used by the admin plane to read LLM provider keys at call time — the
// value is injected into a downstream call and never persisted.
func (v *Vault) ReadKVField(ctx context.Context, relPath, field string) (string, error) {
	if err := v.refreshToken(); err != nil {
		return "", err
	}
	sec, err := v.c.KVv2(v.mount).Get(ctx, relPath)
	if err != nil {
		return "", fmt.Errorf("vault: read %q: %w", relPath, err)
	}
	val, ok := sec.Data[field].(string)
	if !ok {
		return "", fmt.Errorf("vault: secret %q missing string field %q", relPath, field)
	}
	return val, nil
}

// WriteKV writes data to a KV-v2 secret at relPath (under the mount). Used by the
// admin plane to publish an MCP server's deploy descriptor for downstream consumers.
func (v *Vault) WriteKV(ctx context.Context, relPath string, data map[string]any) error {
	if err := v.refreshToken(); err != nil {
		return err
	}
	if _, err := v.c.KVv2(v.mount).Put(ctx, relPath, data); err != nil {
		return fmt.Errorf("vault: write %q: %w", relPath, err)
	}
	return nil
}
