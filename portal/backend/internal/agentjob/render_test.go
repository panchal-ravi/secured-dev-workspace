package agentjob

import (
	"strings"
	"testing"
)

func baseSpec() RenderSpec {
	return RenderSpec{
		JobName:        "agent-project-acme-support-triage",
		Namespace:      "project-acme",
		NodePool:       "agents",
		Image:          "panchalravi/agent-runtime:agentv1",
		ServiceName:    "agent-project-acme-support-triage",
		Tags:           []string{"agent", "agent.name=support-triage"},
		VaultNamespace: "project-acme",
		VaultRole:      "agents",
		AgentName:      "support-triage",
		MCPServers:     []MCPRef{{Name: "github", KVPath: "secret/data/projects/mcp/github"}},
		AgentYAML:      "spec_version: v1\nname: support-triage\ninstructions: |\n  You triage tickets.\n",
	}
}

func TestRenderContainsCoreShape(t *testing.T) {
	hcl := Render(baseSpec())
	for _, want := range []string{
		`job "agent-project-acme-support-triage" {`,
		`namespace   = "project-acme"`,
		`type        = "service"`,
		`node_pool   = "agents"`,
		`image      = "panchalravi/agent-runtime:agentv1"`,
		`to = 8000`, // dynamic host port → container 8000
		`role      = "agents"`,
		`destination     = "local/agent.yaml"`,
		`destination = "secrets/runtime.yaml"`,
		`path     = "/healthz"`,
		`cpu    = 500`,
		`memory = 512`,
		`You triage tickets.`, // verbatim YAML body embedded
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("render missing %q in:\n%s", want, hcl)
		}
	}
	// Dynamic port: no static host-port pin.
	if strings.Contains(hcl, "static =") {
		t.Fatalf("agent job must use a dynamic host port, found a static pin:\n%s", hcl)
	}
}

func TestRenderSecretRefs(t *testing.T) {
	hcl := Render(baseSpec())
	for _, want := range []string{
		`{{ with secret "secret/data/projects/agents/support-triage" }}{{ .Data.data.base_url }}{{ end }}`,
		`{{ with secret "secret/data/projects/agents/support-triage" }}{{ .Data.data.virtual_key }}{{ end }}`,
		`{{ with secret "secret/data/projects/mcp/github" }}{{ .Data.data.url }}{{ end }}`,
		`{{ with secret "secret/data/projects/mcp/github" }}{{ .Data.data.token }}{{ end }}`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("render missing secret ref %q in:\n%s", want, hcl)
		}
	}
}

// The verbatim-YAML template must use non-default delimiters so a user's
// `{{ ... }}` is not interpreted by consul-template.
func TestRenderVerbatimDelimiters(t *testing.T) {
	s := baseSpec()
	s.AgentYAML = "instructions: |\n  Respond with {{name}} verbatim.\n"
	hcl := Render(s)
	if !strings.Contains(hcl, `left_delimiter  = "{~{"`) || !strings.Contains(hcl, `right_delimiter = "}~}"`) {
		t.Fatalf("verbatim template must set custom delimiters:\n%s", hcl)
	}
	if !strings.Contains(hcl, "Respond with {{name}} verbatim.") {
		t.Fatalf("user's {{ }} must be embedded verbatim:\n%s", hcl)
	}
}

// No MCP servers → an explicit empty list, not a dangling `mcp_servers:` key.
func TestRenderNoMCPServers(t *testing.T) {
	s := baseSpec()
	s.MCPServers = nil
	hcl := Render(s)
	if !strings.Contains(hcl, "mcp_servers: []") {
		t.Fatalf("no-servers render must emit an empty list:\n%s", hcl)
	}
	if strings.Contains(hcl, "projects/mcp/") {
		t.Fatalf("no-servers render must not reference any MCP KV:\n%s", hcl)
	}
}
