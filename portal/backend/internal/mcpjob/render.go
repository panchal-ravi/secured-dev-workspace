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
	// DynamicPort maps the container port to a Nomad-assigned host port instead of
	// pinning the host side to Port. Project-plane instances set this so many
	// deployments of the same catalog server (which all share Port) can co-locate on
	// one node without a static host-port collision; the caller resolves the real
	// host port from the placed allocation. The platform reference instance leaves it
	// false (canonical static port, discovery byte-identical to the admin golden).
	DynamicPort bool
}

// Credential is the secret-delivery variant. Only the project-plane WIF shape
// remains (the platform KV/token variants retired with the admin MCP plane).
type Credential interface{ isCredential() }

// WIFCredential renders a project-namespace WIF binding plus a template of
// already-rendered env lines (values carry intact {{ }} consul-template directives).
type WIFCredential struct {
	VaultNamespace string
	WIFRole        string
	EnvTemplates   map[string]string // env var -> consul-template snippet (from CredentialEnv)
}

func (WIFCredential) isCredential() {}

// Render builds the Docker-driver Nomad job HCL.
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
	if !s.DynamicPort {
		w("        static = %d\n", s.Port)
	}
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
