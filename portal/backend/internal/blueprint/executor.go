package blueprint

import (
	"context"
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// ExecutorConfig holds platform-wide values an instantiation needs that are not in
// the credential spec: the per-namespace WIF auth backend path and its bound audience.
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

// Executor instantiates and deprovisions an MCP server's credential spec in a
// project namespace.
type Executor struct {
	v   VaultAdmin
	cfg ExecutorConfig
}

// NewExecutor builds an executor over a VaultAdmin.
func NewExecutor(v VaultAdmin, cfg ExecutorConfig) *Executor {
	return &Executor{v: v, cfg: cfg.withDefaults()}
}

// Instantiate runs the credential recipe for serverName into namespace. It is
// idempotent: a re-instantiate re-applies mounts/config/policy (Vault writes are
// upserts; an existing mount is tolerated). It returns the exact teardown record.
func (e *Executor) Instantiate(ctx context.Context, serverName string, spec CredentialSpec, namespace string, params map[string]string, grants []PathGrant) (InstanceRecord, error) {
	if err := spec.Validate(); err != nil {
		return InstanceRecord{}, err
	}
	if err := requireParams(spec.Params, params); err != nil {
		return InstanceRecord{}, err
	}
	// Validate + render path grants up front, before the recipe touches Vault — a
	// malformed/forbidden grant fails the deploy with no side effects (the dynamic
	// source mounts an engine mid-flow).
	var grantsHCL string
	if len(grants) > 0 {
		if spec.Source == SourceNone {
			return InstanceRecord{}, fmt.Errorf("credential: path grants need a credential source (no WIF token exists to carry them): %w", apperr.ErrBadRequest)
		}
		var err error
		if grantsHCL, err = RenderGrants(grants); err != nil {
			return InstanceRecord{}, err
		}
	}

	rec := InstanceRecord{Namespace: namespace}
	if spec.Source == SourceNone {
		return rec, nil // no policy, no WIF role, nothing to tear down
	}

	name := "mcp-" + serverName // policy name == WIF role name
	rec.WIFRoleName = name
	mount := e.cfg.KVMount // policy-lint anchor for static/wif-token

	switch spec.Source {
	case SourceDynamic:
		d := spec.Dynamic
		mount = strings.TrimSuffix(d.Mount, "/")
		if err := e.v.MountEngine(ctx, namespace, mount, d.Engine, ""); err != nil && !isAlreadyMounted(err) {
			return InstanceRecord{}, err
		}
		for _, w := range d.Configs {
			data, err := interpolateMap(w.Data, params)
			if err != nil {
				return InstanceRecord{}, err
			}
			if err := e.v.WriteLogical(ctx, namespace, mount+"/"+w.Path, data); err != nil {
				return InstanceRecord{}, err
			}
		}
		// Rotate the bootstrap credential out of human knowledge as soon as the
		// engine holds it (e.g. database rotate-root/<conn>, aws config/rotate-root).
		if d.RotateRootPath != "" {
			if err := e.v.WriteLogical(ctx, namespace, mount+"/"+d.RotateRootPath, nil); err != nil {
				return InstanceRecord{}, err
			}
		}
		if d.Role != nil {
			data, err := interpolateMap(d.Role.Data, params)
			if err != nil {
				return InstanceRecord{}, err
			}
			if err := e.v.WriteLogical(ctx, namespace, mount+"/"+d.Role.Path, data); err != nil {
				return InstanceRecord{}, err
			}
		}
		rec.Mounts = []string{mount}
		rec.CredPath = mount + "/" + d.CredsPath
		if d.leaseBased() {
			rec.LeasePrefixes = []string{rec.CredPath}
		}

	case SourceStatic:
		// Seed the write-only secret into the project KV; never logged.
		data, err := interpolateMap(toAnyMap(spec.Static.Data), params)
		if err != nil {
			return InstanceRecord{}, err
		}
		relPath := "projects/mcp-secrets/" + serverName
		if err := e.v.WriteKVv2(ctx, namespace, e.cfg.KVMount, relPath, data); err != nil {
			return InstanceRecord{}, err
		}
		// The job template reads the KV v2 DATA path (the .Data.data.* shape).
		rec.CredPath = e.cfg.KVMount + "/data/" + relPath
		rec.KVPaths = []string{relPath}

	case SourceWIFToken:
		// No engine, no secret; only a policy + WIF binding over the namespace KV.
	}

	// Derive + lint + apply the least-privilege policy (never user-authored).
	policyHCL, err := e.renderPolicy(spec, serverName, mount, grantsHCL)
	if err != nil {
		return InstanceRecord{}, err
	}
	if grantsHCL != "" {
		rec.ExtraGrants = grants
	}
	if err := e.v.WritePolicy(ctx, namespace, name, policyHCL); err != nil {
		return InstanceRecord{}, err
	}
	rec.PolicyNames = []string{name}

	// Bind the Nomad-WIF role to the generated policy.
	if err := e.v.WriteWIFRole(ctx, namespace, e.cfg.AuthPath, name, WIFRole{
		BoundAudiences: []string{e.cfg.BoundAudience},
		UserClaim:      e.cfg.UserClaim,
		TokenPolicies:  []string{name},
		TokenTTL:       spec.tokenTTL(),
	}); err != nil {
		return InstanceRecord{}, err
	}
	return rec, nil
}

// renderPolicy derives + lints the credential's least-privilege policy and appends
// the (already-validated) project-admin grants HCL. The grants are confined to the
// project's Vault namespace and were linted against the deny-list in RenderGrants —
// deliberately outside the base allowlist, which stays tight for the credential's
// own paths. Shared by Instantiate and UpdateGrants so the two write paths never
// diverge.
func (e *Executor) renderPolicy(spec CredentialSpec, serverName, mount, grantsHCL string) (string, error) {
	policyHCL := DeriveCredentialPolicy(spec, e.cfg.KVMount, serverName)
	if err := LintPolicy(policyHCL, allowedPrefixes(e.cfg.KVMount, mount)); err != nil {
		return "", err
	}
	if grantsHCL != "" {
		policyHCL += "\n" + grantsHCL
	}
	return policyHCL, nil
}

// UpdateGrants rewrites a live instance's policy in place with a new set of path
// grants (an empty set reverts to the derived policy alone). Vault evaluates
// policies at request time, so the change applies immediately to outstanding
// tokens — no job restart, no gateway re-wire, no workspace churn. It returns the
// record with ExtraGrants updated; everything else is untouched.
func (e *Executor) UpdateGrants(ctx context.Context, rec InstanceRecord, spec CredentialSpec, serverName string, grants []PathGrant) (InstanceRecord, error) {
	if len(rec.PolicyNames) == 0 {
		return rec, fmt.Errorf("credential: server has no Vault policy to update (source %q): %w", spec.Source, apperr.ErrBadRequest)
	}
	var grantsHCL string
	if len(grants) > 0 {
		var err error
		if grantsHCL, err = RenderGrants(grants); err != nil {
			return rec, err
		}
	}
	mount := e.cfg.KVMount
	if spec.Source == SourceDynamic && spec.Dynamic != nil {
		mount = strings.TrimSuffix(spec.Dynamic.Mount, "/")
	}
	policyHCL, err := e.renderPolicy(spec, serverName, mount, grantsHCL)
	if err != nil {
		return rec, err
	}
	if err := e.v.WritePolicy(ctx, rec.Namespace, rec.PolicyNames[0], policyHCL); err != nil {
		return rec, err
	}
	rec.ExtraGrants = grants
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
	for _, p := range rec.KVPaths {
		if err := e.v.DeleteKVv2Metadata(ctx, rec.Namespace, e.cfg.KVMount, p); err != nil {
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

func requireParams(specs []ParamSpec, params map[string]string) error {
	for _, p := range specs {
		if p.Required {
			if v, ok := params[p.Name]; !ok || v == "" {
				return fmt.Errorf("credential: missing required param %q: %w", p.Name, apperr.ErrBadRequest)
			}
		}
	}
	return nil
}

func toAnyMap(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// allowedPrefixes is the lint allowlist: a dedicated engine mount (the dynamic
// source's engine) may be referenced at its own subtree, plus the project KV
// `projects/` subtree. The shared KV mount is NEVER added as a bare prefix, so a
// static/wif-token policy is confined to projects/ rather than the whole KV mount
// (least privilege). The Vault namespace already isolates the project, so the KV
// path carries no per-project segment.
func allowedPrefixes(kvMount, mount string) []string {
	var out []string
	if mount != kvMount {
		out = append(out, mount+"/")
	}
	kvLogical := kvMount + "/projects/"
	kvData := kvMount + "/data/projects/"
	return append(out, kvLogical, kvData)
}

// isAlreadyMounted lets re-instantiation tolerate an existing mount (idempotency).
func isAlreadyMounted(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already in use")
}
