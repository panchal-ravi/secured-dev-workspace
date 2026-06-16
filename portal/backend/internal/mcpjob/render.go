package mcpjob

import (
	"fmt"
	"sort"
	"strings"
)

// RenderSpec is everything Render needs, fully resolved by the caller (job/service
// names + discovery tags computed by the persona package). Credential selects the
// secret-delivery stanza; nil emits no vault block (e.g. mcp.auth=none, no secrets).
type RenderSpec struct {
	JobName     string
	Namespace   string
	NodePool    string
	Image       string
	Command     []string
	Port        int
	Env         map[string]string
	ServiceName string
	Tags        []string
	Credential  Credential
}

// Credential is the secret-delivery variant: KV (platform secret_refs) or WIF
// (blueprint instance in a project namespace).
type Credential interface{ isCredential() }

// KVCredential renders the existing `vault { role } + template { with secret }`
// path: each env var is read from a Vault KV "path#field" reference.
type KVCredential struct {
	VaultRole  string
	SecretRefs map[string]string // env var -> "path#field"
}

func (KVCredential) isCredential() {}

// WIFCredential renders a project-namespace WIF binding plus a template of
// already-rendered env lines (values carry intact {{ }} consul-template directives).
type WIFCredential struct {
	VaultNamespace string
	WIFRole        string
	EnvTemplates   map[string]string // env var -> consul-template snippet (from CredentialEnv)
}

func (WIFCredential) isCredential() {}

// Render builds the Docker-driver Nomad job HCL. The output for a KVCredential is
// byte-identical to the platform admin's prior renderMCPJobHCL (regression-guarded
// by the admin golden test).
func Render(s RenderSpec) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("job %q {\n", s.JobName)
	w("  namespace   = %q\n", s.Namespace)
	w("  datacenters = [\"dc1\"]\n")
	w("  type        = \"service\"\n")
	if s.NodePool != "" {
		w("  node_pool   = %q\n", s.NodePool)
	}
	w("\n  group \"mcp\" {\n")
	w("    count = 1\n\n")
	w("    network {\n")
	w("      port \"http\" {\n")
	w("        static = %d\n", s.Port)
	w("        to     = %d\n", s.Port)
	w("      }\n")
	w("    }\n\n")

	w("    task \"server\" {\n")
	w("      driver = \"docker\"\n\n")
	w("      config {\n")
	w("        image      = %q\n", s.Image)
	w("        force_pull = true\n")
	w("        ports      = [\"http\"]\n")
	if len(s.Command) > 0 {
		w("        args = [\n")
		for _, a := range s.Command {
			w("          %q,\n", a)
		}
		w("        ]\n")
	}
	w("      }\n")

	if len(s.Env) > 0 {
		w("\n      env {\n")
		for _, k := range sortedKeys(s.Env) {
			w("        %s = %q\n", k, s.Env[k])
		}
		w("      }\n")
	}

	renderCredential(w, s.Credential)

	w("\n      service {\n")
	w("        name     = %q\n", s.ServiceName)
	w("        provider = \"nomad\"\n")
	w("        port     = \"http\"\n")
	w("        tags = [\n")
	for _, t := range s.Tags {
		w("          %q,\n", t)
	}
	w("        ]\n")
	w("        check {\n")
	w("          type     = \"tcp\"\n")
	w("          interval = \"10s\"\n")
	w("          timeout  = \"2s\"\n")
	w("        }\n")
	w("      }\n\n")

	w("      resources {\n")
	w("        cpu    = 250\n")
	w("        memory = 256\n")
	w("      }\n")
	w("    }\n")
	w("  }\n")
	w("}\n")
	return b.String()
}

func renderCredential(w func(string, ...any), c Credential) {
	switch cred := c.(type) {
	case KVCredential:
		if len(cred.SecretRefs) == 0 {
			return
		}
		w("\n      vault {\n")
		w("        role = %q\n", cred.VaultRole)
		w("      }\n\n")
		w("      template {\n")
		w("        destination = \"secrets/secrets.env\"\n")
		w("        env         = true\n")
		w("        change_mode = \"restart\"\n")
		w("        data        = <<EOH\n")
		for _, k := range sortedKeys(cred.SecretRefs) {
			path, field := splitRef(cred.SecretRefs[k])
			w("{{ with secret %q }}%s={{ .Data.data.%s }}{{ end }}\n", path, k, field)
		}
		w("EOH\n")
		w("      }\n")
	case WIFCredential:
		w("\n      vault {\n")
		w("        namespace = %q\n", cred.VaultNamespace)
		w("        role      = %q\n", cred.WIFRole)
		w("      }\n")
		if len(cred.EnvTemplates) == 0 {
			return
		}
		w("\n      template {\n")
		w("        destination = \"secrets/secrets.env\"\n")
		w("        env         = true\n")
		w("        change_mode = \"restart\"\n")
		w("        data        = <<EOH\n")
		for _, k := range sortedKeys(cred.EnvTemplates) {
			w("%s=%s\n", k, cred.EnvTemplates[k])
		}
		w("EOH\n")
		w("      }\n")
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// splitRef parses a "path#field" Vault KV reference; a missing field defaults to
// "value".
func splitRef(ref string) (path, field string) {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "value"
}
