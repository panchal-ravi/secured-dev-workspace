package blueprint

import (
	"context"
	"fmt"
	"time"
)

// ValidationResult is the outcome of validating a blueprint before publish.
type ValidationResult struct {
	Passed  bool      `json:"passed"`
	Checks  []Check   `json:"checks"`
	Message string    `json:"message,omitempty"`
	At      time.Time `json:"at"`
}

// Check is one named gate result.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

// Validator gates blueprint publishing: static lint, then a live consumption-mirror
// test (instantiate into a throwaway namespace, probe 200/403, deprovision).
type Validator struct {
	v  VaultAdmin
	ex *Executor
	// now is injectable for deterministic tests / namespace naming.
	now func() time.Time
}

// NewValidator builds a Validator over a VaultAdmin and an Executor.
func NewValidator(v VaultAdmin, ex *Executor) *Validator {
	return &Validator{v: v, ex: ex, now: time.Now}
}

// Validate runs the publish gate. It returns a ValidationResult (Passed=false on a
// failed gate); it only returns a non-nil error on an unexpected transport failure.
func (val *Validator) Validate(ctx context.Context, m BlueprintManifest) (ValidationResult, error) {
	res := ValidationResult{At: val.now()}

	// Gate 1: shape.
	if err := m.Validate(); err != nil {
		res.Checks = append(res.Checks, Check{Name: "shape", Passed: false, Detail: err.Error()})
		res.Message = "manifest shape invalid"
		return res, nil
	}
	res.Checks = append(res.Checks, Check{Name: "shape", Passed: true})

	// Gate 2: render + lint (no Vault calls yet). Use a representative namespace.
	const probeNS = "bp-validate"
	mount := val.ex.cfg.KVMount
	role := ""
	if m.Class == ClassA {
		mount = render(m.Engines[0].MountPathTpl, probeNS)
		role = render(m.Role.NameTpl, probeNS)
	}
	policyHCL, err := RenderPolicy(m.PolicyTpl, PolicyVars{Namespace: probeNS, Mount: mount, Role: role})
	if err != nil {
		res.Checks = append(res.Checks, Check{Name: "policy-render", Passed: false, Detail: err.Error()})
		res.Message = "policy render failed"
		return res, nil
	}
	if err := LintPolicy(policyHCL, allowedPrefixes(probeNS, val.ex.cfg.KVMount, mount)); err != nil {
		res.Checks = append(res.Checks, Check{Name: "policy-lint", Passed: false, Detail: err.Error()})
		res.Message = "policy lint failed"
		return res, nil
	}
	res.Checks = append(res.Checks, Check{Name: "policy-lint", Passed: true})

	// Gate 3: live consumption-mirror in a throwaway namespace.
	ns := fmt.Sprintf("%s-%s-%d", probeNS, m.ID, val.now().UnixNano())
	if err := val.v.CreateNamespace(ctx, ns); err != nil {
		return res, fmt.Errorf("blueprint: create throwaway namespace: %w", err)
	}
	defer func() { _ = val.v.DeleteNamespace(ctx, ns) }()

	// The blueprint binds a WIF role at auth/<AuthPath>/, so the throwaway namespace
	// needs that JWT auth backend enabled (real project namespaces get it from
	// terraform; a bare validation namespace does not). Enable it for the test and
	// disable it on teardown so the now-empty namespace can be deleted.
	if err := val.v.EnableAuth(ctx, ns, val.ex.cfg.AuthPath, "jwt"); err != nil {
		return res, fmt.Errorf("blueprint: enable %s auth in throwaway namespace: %w", val.ex.cfg.AuthPath, err)
	}
	defer func() { _ = val.v.DisableAuth(ctx, ns, val.ex.cfg.AuthPath) }()

	// Real project namespaces get a kv-v2 `secret` engine from terraform; a bare
	// validation namespace does not. Mount it so Class B/C KV seeds + reads work,
	// and unmount on teardown so the namespace can be deleted.
	if err := val.v.MountKVv2(ctx, ns, val.ex.cfg.KVMount); err != nil {
		return res, fmt.Errorf("blueprint: mount kv in throwaway namespace: %w", err)
	}
	defer func() { _ = val.v.UnmountEngine(ctx, ns, val.ex.cfg.KVMount) }()

	rec, err := val.ex.Instantiate(ctx, m, ns, syntheticParams(m))
	if err != nil {
		res.Checks = append(res.Checks, Check{Name: "instantiate", Passed: false, Detail: err.Error()})
		res.Message = "instantiate failed"
		return res, nil
	}
	defer func() { _ = val.ex.Deprovision(ctx, rec) }()

	// A blueprint that seeds no secret of its own (Class C) has nothing at the path
	// its policy grants, so the allowed-read probe would hit an empty path. Seed a
	// marker there (via the admin client) so the scoped-token read proves the grant
	// works. Class B already has its seeded upstream key at this path.
	if !m.hasSecretParam() {
		markerRel := "projects/" + ns + "/" + m.ID
		if err := val.v.WriteKVv2(ctx, ns, val.ex.cfg.KVMount, markerRel, map[string]any{"probe": "ok"}); err != nil {
			return res, fmt.Errorf("blueprint: seed probe marker: %w", err)
		}
	}

	token, err := val.v.MintTokenWithPolicies(ctx, ns, rec.PolicyNames, "5m")
	if err != nil {
		return res, fmt.Errorf("blueprint: mint probe token: %w", err)
	}

	allowedPath, deniedPath := probePaths(m, ns, mount, role)
	allowedOK, err := val.v.Read(ctx, ns, token, allowedPath)
	if err != nil {
		return res, fmt.Errorf("blueprint: allowed probe: %w", err)
	}
	deniedOK, err := val.v.Read(ctx, ns, token, deniedPath)
	if err != nil {
		return res, fmt.Errorf("blueprint: denied probe: %w", err)
	}
	res.Checks = append(res.Checks,
		Check{Name: "allowed-read-200", Passed: allowedOK},
		Check{Name: "denied-read-403", Passed: !deniedOK},
	)
	res.Passed = allowedOK && !deniedOK
	if !res.Passed {
		res.Message = "consumption-mirror probe failed"
	}
	return res, nil
}

// syntheticParams supplies throwaway values for a validation instantiate. Secret
// params get a dummy value; required strings get a placeholder. Class A's
// connection_url points at a non-routable address (the engine config does not
// connect until a cred is requested, and the probe reads the role, not a cred).
func syntheticParams(m BlueprintManifest) map[string]string {
	p := map[string]string{}
	for _, sp := range m.Params {
		switch sp.Type {
		case "secret":
			p[sp.Name] = "validation-dummy-secret"
		default:
			p[sp.Name] = "validation-placeholder"
		}
	}
	if m.Class == ClassA {
		p["connection_url"] = "postgresql://v:v@127.0.0.1:1/postgres?sslmode=disable"
		p["bootstrap_username"] = "v"
	}
	return p
}

// probePaths returns an allowed path (the brokered cred/KV the policy grants) and a
// denied path (a sys/ read every token must be refused).
func probePaths(m BlueprintManifest, ns, mount, role string) (allowed, denied string) {
	denied = "sys/mounts"
	switch m.Class {
	case ClassA:
		allowed = mount + "/creds/" + role
	default:
		allowed = mount + "/data/projects/" + ns + "/" + m.ID
	}
	return allowed, denied
}
