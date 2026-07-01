// Package jobrender fills the Terraform-style ${...} placeholders in a project's
// raw Nomad job template (stored in Vault KV) at workspace-create time.
//
// The template intentionally mixes three syntaxes:
//   - ${name}      Terraform placeholders the dev-workspace tier fills via
//     templatestring(). The portal fills these instead.
//   - {{ ... }}    consul-template directives, rendered by Nomad at runtime.
//   - bash         the entrypoint script.
//
// Only the ${name} tokens are substituted here; {{ }} and bash are left
// untouched. The placeholder set is documented in the template header
// (terraform/project/templates/dev-workspace.nomad.hcl).
package jobrender

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// placeholderRE matches a single ${identifier} token.
var placeholderRE = regexp.MustCompile(`\$\{[a-zA-Z_][a-zA-Z0-9_]*\}`)

// Render substitutes every ${key} in tmpl with values[key]. It returns an error
// if any ${...} placeholder remains unsubstituted (a missing value catches both
// typos in the template and gaps in the caller's value map). This is the strict
// per-workspace (pass-2) render at launch.
func Render(tmpl string, values map[string]string) (string, error) {
	out := RenderPartial(tmpl, values)
	if leftover := placeholderRE.FindAllString(out, -1); len(leftover) > 0 {
		return "", fmt.Errorf("jobrender: unsubstituted placeholders: %s", strings.Join(dedupe(leftover), ", "))
	}
	return out, nil
}

// RenderPartial substitutes every ${key} in tmpl with values[key], leaving any
// placeholder without a value untouched (unlike Render, which errors). This is the
// project-static (pass-1) substitution done at project-template create: the
// per-workspace tokens must survive for Render at launch.
func RenderPartial(tmpl string, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// Replace longest keys first so a key that is a prefix of another cannot
	// shadow it (defensive; current placeholders are not prefixes of each other).
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })

	pairs := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		pairs = append(pairs, "${"+k+"}", values[k])
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// Placeholders returns the distinct ${...} placeholder names present in tmpl (the
// bare identifiers, without the ${} wrapper).
func Placeholders(tmpl string) []string {
	raw := placeholderRE.FindAllString(tmpl, -1)
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, m := range raw {
		name := m[2 : len(m)-1] // strip "${" and "}"
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
