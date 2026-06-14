package admin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// transport → default MCP path on the server.
var defaultPaths = map[string]string{
	"sse":             "/sse",
	"streamable-http": "/mcp",
}

// renderMCPJobHCL builds a Docker-driver Nomad job for a platform-deployed MCP
// server, following the demo-db-mcp / infra job conventions: a single "mcp" group
// with one static host port mapped to the server's listen port, optional inline
// env, optional Vault-WIF secret env, and a service block carrying the discovery
// tags the portal and gateway use. The server listens on s.Port inside the
// container and is exposed on the same static host port.
func renderMCPJobHCL(s store.MCPServer, cfg Config) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	w("job %q {\n", jobName(s.Name))
	w("  namespace   = %q\n", cfg.MCPNamespace)
	w("  datacenters = [\"dc1\"]\n")
	w("  type        = \"service\"\n")
	if cfg.NodePool != "" {
		w("  node_pool   = %q\n", cfg.NodePool)
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

	if len(s.SecretRefs) > 0 {
		// WIF: the job's Nomad↔Vault role; its policy must allow reading the
		// referenced secret paths. Rendered to a secrets env file, sourced into the
		// task env; a lease rotation restarts the server with the fresh value.
		w("\n      vault {\n")
		w("        role = %q\n", cfg.MCPJobVaultRole)
		w("      }\n\n")
		w("      template {\n")
		w("        destination = \"secrets/secrets.env\"\n")
		w("        env         = true\n")
		w("        change_mode = \"restart\"\n")
		w("        data        = <<EOH\n")
		for _, k := range sortedKeys(s.SecretRefs) {
			path, field := splitRef(s.SecretRefs[k])
			w("{{ with secret %q }}%s={{ .Data.data.%s }}{{ end }}\n", path, k, field)
		}
		w("EOH\n")
		w("      }\n")
	}

	w("\n      service {\n")
	w("        name     = %q\n", serviceName(s.Name))
	w("        provider = \"nomad\"\n")
	w("        port     = \"http\"\n")
	w("        tags = [\n")
	for _, t := range discoveryTags(s) {
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

// discoveryTags returns the Nomad service tags the portal and gateway filter on.
// auth=none in this slice (no wrapper); managed-by=portal marks portal-deployed.
func discoveryTags(s store.MCPServer) []string {
	path := s.Path
	if path == "" {
		path = defaultPaths[s.Transport]
	}
	return []string{
		"mcp",
		"mcp.transport=" + s.Transport,
		"mcp.path=" + path,
		"mcp.auth=none",
		"mcp.scope=platform",
		"mcp.managed-by=portal",
		"mcp.server=" + s.Name,
	}
}

// peerURL builds the ContextForge peer URL for a deployed server on nodeIP.
func peerURL(nodeIP string, s store.MCPServer) string {
	path := s.Path
	if path == "" {
		path = defaultPaths[s.Transport]
	}
	return fmt.Sprintf("http://%s:%d%s", nodeIP, s.Port, path)
}

func jobName(name string) string     { return "mcp-" + name }
func serviceName(name string) string { return "mcp-" + name }

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// splitRef parses a "path#field" Vault KV reference; a missing field defaults to
// the env key's lowercase ("api_key"-style refs use the explicit field form).
func splitRef(ref string) (path, field string) {
	if i := strings.LastIndex(ref, "#"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "value"
}
