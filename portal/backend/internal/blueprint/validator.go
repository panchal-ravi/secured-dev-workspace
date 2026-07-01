package blueprint

import (
	"context"
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

// Validator gates blueprint publishing with static checks only: manifest shape,
// then render + lint of the generated least-privilege policy against the allowed
// path prefixes. There is deliberately no live Vault probe — runtime correctness of
// a recipe is the platform admin's responsibility, verified by deploying the
// blueprint into a real project (the A/B/C reference blueprints).
type Validator struct {
	ex *Executor
	// now is injectable for deterministic tests.
	now func() time.Time
}

// NewValidator builds a Validator over an Executor (used for its config).
func NewValidator(ex *Executor) *Validator {
	return &Validator{ex: ex, now: time.Now}
}

// Validate runs the publish gate: shape, then render + lint. It returns a
// ValidationResult (Passed=false on a failed gate). The error return is reserved for
// unexpected failures and is currently always nil (the gates are static).
func (val *Validator) Validate(_ context.Context, m BlueprintManifest) (ValidationResult, error) {
	res := ValidationResult{At: val.now()}

	// Gate 1: shape.
	if err := m.Validate(); err != nil {
		res.Checks = append(res.Checks, Check{Name: "shape", Passed: false, Detail: err.Error()})
		res.Message = "manifest shape invalid"
		return res, nil
	}
	res.Checks = append(res.Checks, Check{Name: "shape", Passed: true})

	// Gate 2: render + lint the generated policy. A representative namespace is used
	// purely to render the policy template for the lint; nothing is written to Vault.
	const sampleNS = "validation-sample"
	mount := val.ex.cfg.KVMount
	role := ""
	if m.Class == ClassA {
		mount = render(m.Engines[0].MountPathTpl, sampleNS)
		role = render(m.Role.NameTpl, sampleNS)
	}
	policyHCL, err := RenderPolicy(m.PolicyTpl, PolicyVars{Namespace: sampleNS, Mount: mount, Role: role})
	if err != nil {
		res.Checks = append(res.Checks, Check{Name: "policy-render", Passed: false, Detail: err.Error()})
		res.Message = "policy render failed"
		return res, nil
	}
	if err := LintPolicy(policyHCL, allowedPrefixes(val.ex.cfg.KVMount, mount)); err != nil {
		res.Checks = append(res.Checks, Check{Name: "policy-lint", Passed: false, Detail: err.Error()})
		res.Message = "policy lint failed"
		return res, nil
	}
	res.Checks = append(res.Checks, Check{Name: "policy-lint", Passed: true})

	res.Passed = true
	return res, nil
}
