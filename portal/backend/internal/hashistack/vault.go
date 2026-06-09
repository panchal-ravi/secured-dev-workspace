package hashistack

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	vapi "github.com/hashicorp/vault/api"

	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
)

// descTTL is how long project listings and descriptors are cached. Descriptors
// change only when a project is (re)onboarded via Terraform, so a short TTL cuts
// Vault load and latency on the hot list/get paths with negligible staleness.
const descTTL = 30 * time.Second

// Vault wraps the KV-v2 reads the portal needs: enumerate onboarded projects and
// read each project's portal descriptor. Reads are served from a small TTL cache.
type Vault struct {
	c         *vapi.Client
	mount     string
	tokenFile string // when set (WIF deploy), re-read before each call so a rotated token is picked up

	mu       sync.Mutex
	projects cachedProjects
	descs    map[string]cachedDescriptor
}

type cachedProjects struct {
	names []string
	exp   time.Time
}

type cachedDescriptor struct {
	d   descriptor.Descriptor
	exp time.Time
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
	v := &Vault{c: c, mount: mount, tokenFile: tokenFile, descs: map[string]cachedDescriptor{}}
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

// Ping checks Vault is reachable (unauthenticated health endpoint), for readiness.
func (v *Vault) Ping(ctx context.Context) error {
	if _, err := v.c.Sys().HealthWithContext(ctx); err != nil {
		return fmt.Errorf("vault: health: %w", err)
	}
	return nil
}

// ListProjects lists project names under <mount>/projects/ (KV-v2 metadata).
func (v *Vault) ListProjects(ctx context.Context) ([]string, error) {
	v.mu.Lock()
	if v.projects.names != nil && time.Now().Before(v.projects.exp) {
		names := v.projects.names
		v.mu.Unlock()
		return names, nil
	}
	v.mu.Unlock()

	if err := v.refreshToken(); err != nil {
		return nil, err
	}
	sec, err := v.c.Logical().ListWithContext(ctx, v.mount+"/metadata/projects")
	if err != nil {
		return nil, fmt.Errorf("vault: list projects: %w", err)
	}
	names := []string{}
	if sec != nil && sec.Data != nil {
		if raw, ok := sec.Data["keys"].([]interface{}); ok {
			for _, k := range raw {
				if s, ok := k.(string); ok {
					names = append(names, strings.TrimSuffix(s, "/"))
				}
			}
		}
	}
	v.mu.Lock()
	v.projects = cachedProjects{names: names, exp: time.Now().Add(descTTL)}
	v.mu.Unlock()
	return names, nil
}

// ReadDescriptor reads and parses one project's portal descriptor (TTL-cached).
func (v *Vault) ReadDescriptor(ctx context.Context, project string) (descriptor.Descriptor, error) {
	v.mu.Lock()
	if e, ok := v.descs[project]; ok && time.Now().Before(e.exp) {
		v.mu.Unlock()
		return e.d, nil
	}
	v.mu.Unlock()

	if err := v.refreshToken(); err != nil {
		return descriptor.Descriptor{}, err
	}
	sec, err := v.c.KVv2(v.mount).Get(ctx, "projects/"+project+"/portal-descriptor")
	if err != nil {
		return descriptor.Descriptor{}, fmt.Errorf("vault: read descriptor %q: %w", project, err)
	}
	raw, ok := sec.Data["descriptor"].(string)
	if !ok {
		return descriptor.Descriptor{}, fmt.Errorf("vault: descriptor %q: missing 'descriptor' key", project)
	}
	d, err := descriptor.Parse(raw)
	if err != nil {
		return descriptor.Descriptor{}, err
	}
	v.mu.Lock()
	v.descs[project] = cachedDescriptor{d: d, exp: time.Now().Add(descTTL)}
	v.mu.Unlock()
	return d, nil
}

// ReadJobTemplate returns the raw job-spec HCL for a project's flavor (the
// dev-workspace tier reads the same KV secret). Not cached: read once per create.
func (v *Vault) ReadJobTemplate(ctx context.Context, project, flavor string) (string, error) {
	if err := v.refreshToken(); err != nil {
		return "", err
	}
	sec, err := v.c.KVv2(v.mount).Get(ctx, "projects/"+project+"/job-templates/"+flavor)
	if err != nil {
		return "", fmt.Errorf("vault: read job template %q/%q: %w", project, flavor, err)
	}
	jobspec, ok := sec.Data["jobspec"].(string)
	if !ok {
		return "", fmt.Errorf("vault: job template %q/%q: missing 'jobspec' key", project, flavor)
	}
	return jobspec, nil
}

// ListDescriptors reads every project that has a portal descriptor. Projects
// without one (or unreadable) are skipped so one bad project can't hide the rest.
func (v *Vault) ListDescriptors(ctx context.Context) ([]descriptor.Descriptor, error) {
	projects, err := v.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]descriptor.Descriptor, 0, len(projects))
	for _, p := range projects {
		d, err := v.ReadDescriptor(ctx, p)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}
