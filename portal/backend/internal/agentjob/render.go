// Package agentjob renders the Nomad job HCL for a deployed AI agent. It is a
// sibling of mcpjob rather than an extension of it: the agent delivers its config
// through two file-based template stanzas (a verbatim YAML config file and a
// Vault-rendered secrets file), which does not fit mcpjob's env-file WIF shape.
package agentjob

import (
	"fmt"
	"strings"
)

// LLM KV (secret/data/projects/agents/<agent>) fields written by the agents
// service. Each MCP server's {url, token} is read from a KV path the caller
// supplies (MCPRef.KVPath) — a per-template subset-scoped token for templates,
// the shared projects/mcp/<server> token for whole-server wiring. All are read
// via WIF at render time.
const (
	agentKVPath   = "secret/data/projects/agents/"
	containerPort = 8000
	// heredocMarker delimits the verbatim-YAML template body. Any agent YAML line
	// equal to it would prematurely close the heredoc, so the YAML validator
	// rejects such lines.
	heredocMarker = "EOAGENTYAML"
	// The verbatim YAML template uses non-default consul-template delimiters so a
	// user's `{{ ... }}` (e.g. in agent instructions) passes through literally
	// instead of being interpreted. The validator rejects the sentinel so user
	// content can never inject a directive.
	ctlLeft  = "{~{"
	ctlRight = "}~}"
)

// RenderSpec is everything Render needs, fully resolved by the caller (job/service
// names + discovery tags computed by the agents service).
type RenderSpec struct {
	JobName        string
	Namespace      string // Nomad namespace (the project's)
	NodePool       string
	Image          string
	ServiceName    string
	Tags           []string
	VaultNamespace string
	VaultRole      string   // per-project WIF role "agents"
	AgentName      string   // LLM KV key: projects/agents/<AgentName>
	MCPServers     []MCPRef // selected servers with their per-server KV paths
	AgentYAML      string   // verbatim user YAML → local/agent.yaml
}

// MCPRef names one MCP server the agent wires and the Vault KV v2 data path where
// its {url, token} live (a subset-scoped token for a template, or the shared
// projects/mcp/<server> token). KVPath is a full path, e.g.
// "secret/data/projects/agents/tmpl-triage/mcp/github".
type MCPRef struct {
	Name   string
	KVPath string
}

// Render builds the Docker-driver Nomad service job HCL for one agent.
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
	w("\n  group \"agent\" {\n")
	w("    count = 1\n\n")
	// Dynamic host port → container :8000, so many agents co-locate on the pool.
	w("    network {\n")
	w("      port \"http\" {\n")
	w("        to = %d\n", containerPort)
	w("      }\n")
	w("    }\n\n")

	w("    task \"agent\" {\n")
	w("      driver = \"docker\"\n\n")
	w("      config {\n")
	w("        image      = %q\n", s.Image)
	w("        force_pull = true\n")
	w("        ports      = [\"http\"]\n")
	w("      }\n\n")

	w("      env {\n")
	w("        AGENT_CONFIG  = \"/local/agent.yaml\"\n")
	w("        AGENT_RUNTIME = \"/secrets/runtime.yaml\"\n")
	w("      }\n\n")

	// WIF binding: this job's identity exchanges for a token under the per-project
	// "agents" role, whose policy reads only the agent LLM key + selected MCP KV.
	w("      vault {\n")
	w("        namespace = %q\n", s.VaultNamespace)
	w("        role      = %q\n", s.VaultRole)
	w("      }\n\n")

	renderAgentYAMLTemplate(w, s.AgentYAML)
	renderRuntimeTemplate(w, s.AgentName, s.MCPServers)

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
	w("          type     = \"http\"\n")
	w("          path     = \"/healthz\"\n")
	w("          interval = \"10s\"\n")
	w("          timeout  = \"2s\"\n")
	w("        }\n")
	w("      }\n\n")

	w("      resources {\n")
	w("        cpu    = 500\n")
	w("        memory = 512\n")
	w("      }\n")
	w("    }\n")
	w("  }\n")
	w("}\n")
	return b.String()
}

// renderAgentYAMLTemplate emits the verbatim-YAML template stanza. Custom
// delimiters neutralize consul-template so the user's YAML (which may contain
// `{{ }}`) is written unchanged.
func renderAgentYAMLTemplate(w func(string, ...any), yaml string) {
	w("      template {\n")
	w("        destination     = \"local/agent.yaml\"\n")
	w("        left_delimiter  = %q\n", ctlLeft)
	w("        right_delimiter = %q\n", ctlRight)
	w("        change_mode     = \"restart\"\n")
	w("        data            = <<%s\n", heredocMarker)
	w("%s\n", strings.TrimRight(yaml, "\n"))
	w("%s\n", heredocMarker)
	w("      }\n")
}

// renderRuntimeTemplate emits the Vault-rendered secrets file: the agent's LLM
// base URL + per-agent key, and each selected MCP server's URL + scoped token.
// Values are double-quoted so colons/special characters survive YAML parsing.
func renderRuntimeTemplate(w func(string, ...any), agentName string, servers []MCPRef) {
	w("\n      template {\n")
	w("        destination = \"secrets/runtime.yaml\"\n")
	w("        change_mode = \"restart\"\n")
	w("        data        = <<EOH\n")
	w("llm:\n")
	w("  base_url: \"%s\"\n", secretRef(agentKVPath+agentName, "base_url"))
	w("  api_key: \"%s\"\n", secretRef(agentKVPath+agentName, "virtual_key"))
	if len(servers) == 0 {
		w("mcp_servers: []\n")
	} else {
		w("mcp_servers:\n")
		for _, m := range servers {
			w("  - name: %q\n", m.Name)
			w("    transport: sse\n")
			w("    url: \"%s\"\n", secretRef(m.KVPath, "url"))
			w("    token: \"%s\"\n", secretRef(m.KVPath, "token"))
		}
	}
	w("EOH\n")
	w("      }\n")
}

// secretRef is a consul-template Vault KV v2 lookup for one field.
func secretRef(path, field string) string {
	return fmt.Sprintf("{{ with secret %q }}{{ .Data.data.%s }}{{ end }}", path, field)
}
