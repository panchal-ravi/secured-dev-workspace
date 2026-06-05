package hashistack

import (
	"context"
	"fmt"
	"strings"

	vapi "github.com/hashicorp/vault/api"

	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
)

// Vault wraps the KV-v2 reads the portal needs: enumerate onboarded projects and
// read each project's portal descriptor.
type Vault struct {
	c     *vapi.Client
	mount string
}

func NewVault(addr, token, mount string) (*Vault, error) {
	cfg := vapi.DefaultConfig()
	cfg.Address = addr
	if err := cfg.ConfigureTLS(&vapi.TLSConfig{Insecure: true}); err != nil {
		return nil, fmt.Errorf("vault: configure tls: %w", err)
	}
	c, err := vapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("vault: new client: %w", err)
	}
	c.SetToken(token)
	return &Vault{c: c, mount: mount}, nil
}

// ListProjects lists project names under <mount>/projects/ (KV-v2 metadata).
func (v *Vault) ListProjects(ctx context.Context) ([]string, error) {
	sec, err := v.c.Logical().ListWithContext(ctx, v.mount+"/metadata/projects")
	if err != nil {
		return nil, fmt.Errorf("vault: list projects: %w", err)
	}
	if sec == nil || sec.Data == nil {
		return nil, nil
	}
	raw, ok := sec.Data["keys"].([]interface{})
	if !ok {
		return nil, nil
	}
	names := make([]string, 0, len(raw))
	for _, k := range raw {
		if s, ok := k.(string); ok {
			names = append(names, strings.TrimSuffix(s, "/"))
		}
	}
	return names, nil
}

// ReadDescriptor reads and parses one project's portal descriptor.
func (v *Vault) ReadDescriptor(ctx context.Context, project string) (descriptor.Descriptor, error) {
	sec, err := v.c.KVv2(v.mount).Get(ctx, "projects/"+project+"/portal-descriptor")
	if err != nil {
		return descriptor.Descriptor{}, fmt.Errorf("vault: read descriptor %q: %w", project, err)
	}
	raw, ok := sec.Data["descriptor"].(string)
	if !ok {
		return descriptor.Descriptor{}, fmt.Errorf("vault: descriptor %q: missing 'descriptor' key", project)
	}
	return descriptor.Parse(raw)
}

// ReadJobTemplate returns the raw job-spec HCL for a project's flavor (the
// dev-workspace tier reads the same KV secret).
func (v *Vault) ReadJobTemplate(ctx context.Context, project, flavor string) (string, error) {
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
