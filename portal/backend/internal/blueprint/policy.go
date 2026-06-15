package blueprint

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/hashicorp/hcl"
	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// PolicyVars are the values a policy template is rendered with.
type PolicyVars struct {
	Namespace string
	Mount     string // e.g. "database/acme"
	Role      string // e.g. "dev-workspace-ro"
}

// RenderPolicy renders an HCL policy template. Templates use Go text/template
// syntax with {{.Namespace}} {{.Mount}} {{.Role}}. Missing keys are an error so a
// typo can't silently emit an empty path.
func RenderPolicy(tpl string, v PolicyVars) (string, error) {
	t, err := template.New("policy").Option("missingkey=error").Parse(tpl)
	if err != nil {
		return "", fmt.Errorf("blueprint: parse policy template: %w: %v", apperr.ErrBadRequest, err)
	}
	var b bytes.Buffer
	if err := t.Execute(&b, v); err != nil {
		return "", fmt.Errorf("blueprint: render policy template: %w: %v", apperr.ErrBadRequest, err)
	}
	return b.String(), nil
}

// deniedPrefixes are control-plane mounts a generated policy may never touch.
var deniedPrefixes = []string{"sys/", "auth/", "identity/", "cubbyhole/"}

// LintPolicy is the syntax gate + security lint over a *rendered* policy. It (1)
// parses the HCL (rejecting malformed output) and (2) asserts every path rule
// targets one of allowedMountPrefixes and uses no escalating capability. This is
// the structural guarantee that a buggy blueprint can't escape its tenant.
func LintPolicy(rendered string, allowedMountPrefixes []string) error {
	// 1. Syntax gate — parse as HCL; malformed input errors here.
	var root map[string]any
	if err := hcl.Decode(&root, rendered); err != nil {
		return fmt.Errorf("blueprint: policy is not valid HCL: %w: %v", apperr.ErrBadRequest, err)
	}
	paths, err := pathRules(rendered)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("blueprint: policy defines no path rules: %w", apperr.ErrBadRequest)
	}
	// 2. Security lint.
	for _, pr := range paths {
		p := strings.TrimPrefix(pr.path, "/")
		// Reject path traversal: a ".." segment defeats the prefix checks below (a path
		// like "database/acme/../sys/mounts" would pass the allowed-prefix test yet point
		// outside the tenant's mounts). Vault doesn't filesystem-collapse policy paths, so
		// the lint must refuse them outright rather than emit a false-safe verdict.
		if strings.Contains(p, "..") {
			return fmt.Errorf("blueprint: policy path %q contains a traversal segment: %w", p, apperr.ErrForbidden)
		}
		for _, d := range deniedPrefixes {
			if strings.HasPrefix(p, d) {
				return fmt.Errorf("blueprint: policy targets forbidden prefix %q: %w", d, apperr.ErrForbidden)
			}
		}
		if strings.HasPrefix(p, "*") {
			return fmt.Errorf("blueprint: policy uses a root glob path: %w", apperr.ErrForbidden)
		}
		if !hasAllowedPrefix(p, allowedMountPrefixes) {
			return fmt.Errorf("blueprint: policy path %q is outside the instance's own mounts %v: %w", p, allowedMountPrefixes, apperr.ErrForbidden)
		}
		for _, c := range pr.caps {
			if c == "sudo" || c == "root" {
				return fmt.Errorf("blueprint: policy requests escalating capability %q: %w", c, apperr.ErrForbidden)
			}
		}
	}
	return nil
}

func hasAllowedPrefix(p string, allowed []string) bool {
	for _, a := range allowed {
		if strings.HasPrefix(p, strings.TrimPrefix(a, "/")) {
			return true
		}
	}
	return false
}

type pathRule struct {
	path string
	caps []string
}

// pathRules extracts (path, capabilities) pairs from a rendered Vault policy.
// hcl.Decode into a typed shape loses the block labels, so decode into the generic
// object tree Vault itself uses: a top-level "path" object keyed by the path.
func pathRules(rendered string) ([]pathRule, error) {
	var obj struct {
		Path []map[string]struct {
			Capabilities []string `hcl:"capabilities"`
		} `hcl:"path"`
	}
	if err := hcl.Decode(&obj, rendered); err != nil {
		return nil, fmt.Errorf("blueprint: decode policy paths: %w: %v", apperr.ErrBadRequest, err)
	}
	var out []pathRule
	for _, block := range obj.Path {
		for path, body := range block {
			out = append(out, pathRule{path: path, caps: body.Capabilities})
		}
	}
	return out, nil
}
