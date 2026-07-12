package jobtemplate

import (
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestInject_MCPAndEngines(t *testing.T) {
	base := `job "x" {
  task "workspace" {
      # @project-addons:secrets

      template { destination = "secrets/llm-key" }
      config { command = "/local/entrypoint.sh" }
      data = <<EOH
#!/bin/bash
sudo -u dev git config --global user.email "${developer_email}"
# @project-addons:entrypoint

exec /usr/sbin/sshd -D -e
EOH
  }
}`
	addons := store.TemplateAddons{
		MCPServers: []string{"demo-db"},
		Engines: []store.AddonEngine{{
			Mount: "kv-tools", Type: "kv-v2", KVPath: "secret/data/projects/tools",
			SecretFiles: []store.AddonSecretFile{{KVField: "api_key", DestFile: "tools-key"}},
		}},
	}
	got := Inject(base, addons)

	// MCP secret templates + engine secret template injected at the secrets marker.
	for _, want := range []string{
		`destination = "secrets/mcp-demo-db-url"`,
		`destination = "secrets/mcp-demo-db-token"`,
		`{{ with secret "secret/data/projects/mcp/demo-db" }}{{ .Data.data.url }}{{ end }}`,
		`destination = "secrets/tools-key"`,
		`{{ with secret "secret/data/projects/tools" }}{{ .Data.data.api_key }}{{ end }}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("injected source missing %q\n---\n%s", want, got)
		}
	}
	// claude mcp add injected at the entrypoint marker.
	if !strings.Contains(got, `claude mcp add --scope user --transport sse demo-db "$(cat /secrets/mcp-demo-db-url)"`) {
		t.Errorf("entrypoint MCP registration not injected:\n%s", got)
	}
	// Markers consumed; pass-2 placeholder + consul-template preserved.
	if strings.Contains(got, "@project-addons") {
		t.Errorf("markers not replaced:\n%s", got)
	}
	if !strings.Contains(got, `${developer_email}`) {
		t.Errorf("pass-2 placeholder disturbed:\n%s", got)
	}
}

func TestInject_EmptyAddonsRemovesMarkers(t *testing.T) {
	base := "a\n# @project-addons:secrets\nb\n# @project-addons:entrypoint\nc"
	got := Inject(base, store.TemplateAddons{})
	if strings.Contains(got, "@project-addons") {
		t.Fatalf("markers should be replaced (with empty content):\n%s", got)
	}
}
