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
// typos in the template and gaps in the caller's value map).
func Render(tmpl string, values map[string]string) (string, error) {
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
	out := strings.NewReplacer(pairs...).Replace(tmpl)

	if leftover := placeholderRE.FindAllString(out, -1); len(leftover) > 0 {
		return "", fmt.Errorf("jobrender: unsubstituted placeholders: %s", strings.Join(dedupe(leftover), ", "))
	}
	return out, nil
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
