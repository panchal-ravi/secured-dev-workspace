// Package mcpjob renders Docker-driver Nomad jobs for deployed MCP servers and
// fills the blueprint job-credential templates. It is persona-neutral: both the
// platform-admin plane and the project-admin plane render through it.
package mcpjob

import (
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
)

// CredentialEnv fills the render-time ${...} tokens in a blueprint's job-credential
// env templates from the InstanceRecord + deploy params, leaving every {{ }}
// consul-template directive for Nomad. The exposed tokens are ${namespace},
// ${mount} (first engine mount, "" when none), ${cred_path} (the credential read
// path the policy grants), ${wif_role}, plus every deploy param. An unresolved
// ${token} is an error (it means the manifest references something not provided).
func CredentialEnv(jc blueprint.JobCredentialSpec, rec blueprint.InstanceRecord, params map[string]string) (map[string]string, error) {
	values := map[string]string{
		"namespace": rec.Namespace,
		"wif_role":  rec.WIFRoleName,
		"mount":     first(rec.Mounts),
		"cred_path": first(rec.LeasePrefixes),
	}
	for k, v := range params {
		values[k] = v
	}
	out := make(map[string]string, len(jc.EnvTemplates))
	for name, tpl := range jc.EnvTemplates {
		rendered, err := jobrender.Render(tpl, values)
		if err != nil {
			return nil, err
		}
		out[name] = rendered
	}
	return out, nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
