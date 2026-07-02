// Package jobtemplate owns the portal's Nomad workspace job templates: the three
// base templates (standard / GPU / microVM) seeded into the Postgres store, and
// the placeholder contract shared by the two render passes. A template mixes 12
// project-static ${...} placeholders (filled at project-template create, pass-1)
// and 5 per-workspace ${...} placeholders (filled by jobrender at launch, pass-2);
// consul-template {{ }} and bash $(...) are left untouched by both.
package jobtemplate

import (
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
)

// ProjectStaticPlaceholders are filled at project-template create (pass-1) from
// project-admin input + values derived by convention from the project namespace.
var ProjectStaticPlaceholders = []string{
	"namespace", "image", "git_repo_url", "wif_role", "vault_namespace",
	"ssh_ca_path", "github_token_path", "mcp_kv_path", "llm_kv_path", "llm_base_url",
	"llm_model_primary", "llm_model_fast",
}

// PerWorkspacePlaceholders are filled by jobrender.Render at workspace launch (pass-2).
var PerWorkspacePlaceholders = []string{
	"job_name", "ssh_port", "volume_name", "developer_email", "git_user_name",
}

// allowedPlaceholders is the union set a template may reference.
func allowedPlaceholders() map[string]bool {
	m := make(map[string]bool, len(ProjectStaticPlaceholders)+len(PerWorkspacePlaceholders))
	for _, k := range ProjectStaticPlaceholders {
		m[k] = true
	}
	for _, k := range PerWorkspacePlaceholders {
		m[k] = true
	}
	return m
}

// ValidatePlaceholders is the correctness gate for a template's source: every
// ${...} token must be one of the 15 known placeholders. An unknown token would
// survive both passes and make jobrender.Render fail at launch (a token that is
// never substituted), so it is rejected at author/publish time instead. Returns
// ErrBadRequest naming the offending tokens.
func ValidatePlaceholders(src string) error {
	allowed := allowedPlaceholders()
	var unknown []string
	for _, name := range jobrender.Placeholders(src) {
		if !allowed[name] {
			unknown = append(unknown, "${"+name+"}")
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: unknown template placeholders: %s", apperr.ErrBadRequest, strings.Join(unknown, ", "))
	}
	return nil
}
