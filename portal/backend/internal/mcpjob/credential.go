// Package mcpjob renders Docker-driver Nomad jobs for deployed MCP servers and
// fills the blueprint job-credential templates. It is persona-neutral: both the
// platform-admin plane and the project-admin plane render through it.
package mcpjob

import (
	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
)

// CredentialEnv fills the render-time ${...} tokens in a credential spec's
// env templates from the InstanceRecord + deploy params, leaving every {{ }}
// consul-template directive for Nomad. The exposed tokens are ${namespace},
// ${mount} (first engine mount, "" when none), ${cred_path} (the credential read
// path the policy grants), ${wif_role}, plus every deploy param. An unresolved
// ${token} is an error (it means the spec references something not provided).
func CredentialEnv(envTemplates map[string]string, rec blueprint.InstanceRecord, params map[string]string) (map[string]string, error) {
	values := map[string]string{
		"namespace": rec.Namespace,
		"wif_role":  rec.WIFRoleName,
		"mount":     first(rec.Mounts),
		"cred_path": credPath(rec),
	}
	for k, v := range params {
		values[k] = v
	}
	out := make(map[string]string, len(envTemplates))
	for name, tpl := range envTemplates {
		rendered, err := jobrender.Render(tpl, values)
		if err != nil {
			return nil, err
		}
		out[name] = rendered
	}
	return out, nil
}

// credPath prefers the record's explicit CredPath; records persisted before the
// field existed (legacy Class A only) fall back to the first lease prefix, which
// for those is the same string — so legacy blobs render identically.
func credPath(rec blueprint.InstanceRecord) string {
	if rec.CredPath != "" {
		return rec.CredPath
	}
	return first(rec.LeasePrefixes)
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
