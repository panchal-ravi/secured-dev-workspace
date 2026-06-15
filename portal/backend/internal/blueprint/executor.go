package blueprint

import (
	"context"
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// ExecutorConfig holds platform-wide values an instantiation needs that are not in
// the manifest: the per-namespace WIF auth backend path and its bound audience.
type ExecutorConfig struct {
	AuthPath      string // e.g. "jwt-nomad"
	BoundAudience string // e.g. "vault"
	UserClaim     string // e.g. "nomad_job_id"
	KVMount       string // project runtime-coordinate KV mount, e.g. "secret"
}

func (c ExecutorConfig) withDefaults() ExecutorConfig {
	if c.AuthPath == "" {
		c.AuthPath = "jwt-nomad"
	}
	if c.UserClaim == "" {
		c.UserClaim = "nomad_job_id"
	}
	if c.KVMount == "" {
		c.KVMount = "secret"
	}
	return c
}

// Executor instantiates and deprovisions a manifest in a project namespace.
type Executor struct {
	v   VaultAdmin
	cfg ExecutorConfig
}

// NewExecutor builds an executor over a VaultAdmin.
func NewExecutor(v VaultAdmin, cfg ExecutorConfig) *Executor {
	return &Executor{v: v, cfg: cfg.withDefaults()}
}

// render renders a manifest template string (mount paths, role/policy names) for a
// namespace. Only {{.Namespace}} is available at this stage.
func render(tpl, namespace string) string {
	return strings.ReplaceAll(tpl, "{{.Namespace}}", namespace)
}

// Instantiate runs the class-specific recipe into namespace. It is idempotent: a
// re-instantiate re-applies mounts/config/policy (Vault writes are upserts; a mount
// that already exists is tolerated). It returns the exact teardown record.
func (e *Executor) Instantiate(ctx context.Context, m BlueprintManifest, namespace string, params map[string]string) (InstanceRecord, error) {
	if err := m.Validate(); err != nil {
		return InstanceRecord{}, err
	}
	if err := requireParams(m, params); err != nil {
		return InstanceRecord{}, err
	}
	policyName := render(m.WIFRole.NameTpl, namespace)
	wifRoleName := render(m.WIFRole.NameTpl, namespace)
	rec := InstanceRecord{Ref: m.Ref(), Namespace: namespace, WIFRoleName: wifRoleName}

	var mount, roleName string
	switch m.Class {
	case ClassA:
		eng := m.Engines[0]
		mount = render(eng.MountPathTpl, namespace)
		roleName = render(m.Role.NameTpl, namespace)
		if err := e.v.MountEngine(ctx, namespace, mount, "database", ""); err != nil && !isAlreadyMounted(err) {
			return InstanceRecord{}, err
		}
		if err := e.v.ConfigureDBConnection(ctx, namespace, mount, "conn", DBConnectionConfig{
			Plugin:        eng.Plugin,
			ConnectionURL: params["connection_url"],
			Username:      params["bootstrap_username"],
			Password:      secretParam(m, params),
			AllowedRoles:  []string{roleName},
		}); err != nil {
			return InstanceRecord{}, err
		}
		// Immediately rotate the bootstrap admin cred out of human knowledge (D5).
		if err := e.v.RotateRoot(ctx, namespace, mount, "conn"); err != nil {
			return InstanceRecord{}, err
		}
		if err := e.v.WriteDBRole(ctx, namespace, mount, roleName, DBRole{
			DBName:             "conn",
			CreationStatements: m.Role.CreationStatements,
			DefaultTTLSeconds:  m.Role.DefaultTTLSeconds,
			MaxTTLSeconds:      m.Role.MaxTTLSeconds,
		}); err != nil {
			return InstanceRecord{}, err
		}
		rec.Mounts = []string{mount}
		rec.LeasePrefixes = []string{mount + "/creds/" + roleName}

	case ClassB:
		// Seed the write-only upstream secret into the project KV; never logged.
		relPath := "projects/" + namespace + "/" + m.ID
		if err := e.v.WriteKVv2(ctx, namespace, e.cfg.KVMount, relPath, map[string]any{
			"api_key": secretParam(m, params),
		}); err != nil {
			return InstanceRecord{}, err
		}
		mount = e.cfg.KVMount
		roleName = relPath // policy targets this KV path

	case ClassC:
		// No engine, no secret; only a policy + WIF binding over the namespace mounts.
		mount = e.cfg.KVMount
		roleName = ""
	}

	// Generate + lint + apply the least-privilege policy.
	policyHCL, err := RenderPolicy(m.PolicyTpl, PolicyVars{Namespace: namespace, Mount: mount, Role: roleName})
	if err != nil {
		return InstanceRecord{}, err
	}
	if err := LintPolicy(policyHCL, allowedPrefixes(namespace, e.cfg.KVMount, mount)); err != nil {
		return InstanceRecord{}, err
	}
	if err := e.v.WritePolicy(ctx, namespace, policyName, policyHCL); err != nil {
		return InstanceRecord{}, err
	}
	rec.PolicyNames = []string{policyName}

	// Bind the Nomad-WIF role to the generated policy.
	if err := e.v.WriteWIFRole(ctx, namespace, e.cfg.AuthPath, wifRoleName, WIFRole{
		BoundAudiences: []string{e.cfg.BoundAudience},
		UserClaim:      e.cfg.UserClaim,
		TokenPolicies:  []string{policyName},
		TokenTTL:       m.WIFRole.TokenTTL,
	}); err != nil {
		return InstanceRecord{}, err
	}
	return rec, nil
}

// Deprovision tears an instance down in lease-safe order: revoke every dynamic
// lease prefix FIRST, then delete the policy + WIF role, then unmount engines. This
// is the structural fix for the unmount-with-live-leases failure.
func (e *Executor) Deprovision(ctx context.Context, rec InstanceRecord) error {
	var errs []string
	for _, p := range rec.LeasePrefixes {
		if err := e.v.RevokeLeasesByPrefix(ctx, rec.Namespace, p); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, n := range rec.PolicyNames {
		if err := e.v.DeletePolicy(ctx, rec.Namespace, n); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if rec.WIFRoleName != "" {
		if err := e.v.DeleteWIFRole(ctx, rec.Namespace, e.cfg.AuthPath, rec.WIFRoleName); err != nil {
			errs = append(errs, err.Error())
		}
	}
	for _, mt := range rec.Mounts {
		if err := e.v.UnmountEngine(ctx, rec.Namespace, mt); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("blueprint: deprovision had %d error(s): %s", len(errs), strings.Join(errs, "; "))
	}
	return nil
}

// ---- helpers ----

func requireParams(m BlueprintManifest, params map[string]string) error {
	for _, p := range m.Params {
		if p.Required {
			if v, ok := params[p.Name]; !ok || v == "" {
				return fmt.Errorf("blueprint: missing required param %q: %w", p.Name, apperr.ErrBadRequest)
			}
		}
	}
	return nil
}

func secretParam(m BlueprintManifest, params map[string]string) string {
	for _, p := range m.Params {
		if p.Type == "secret" {
			return params[p.Name]
		}
	}
	return ""
}

// allowedPrefixes is the lint allowlist: a dedicated engine mount (Class A's
// database engine) may be referenced at its own subtree, plus the instance's
// per-namespace slice of the project KV. The shared KV mount is NEVER added as a
// bare prefix, so a Class B/C policy is confined to projects/<ns>/ within its
// namespace rather than the whole KV mount (least privilege).
func allowedPrefixes(namespace, kvMount, mount string) []string {
	var out []string
	if mount != kvMount {
		out = append(out, mount+"/")
	}
	kvLogical := kvMount + "/projects/" + namespace + "/"
	kvData := kvMount + "/data/projects/" + namespace + "/"
	return append(out, kvLogical, kvData)
}

// isAlreadyMounted lets re-instantiation tolerate an existing mount (idempotency).
func isAlreadyMounted(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already in use")
}
