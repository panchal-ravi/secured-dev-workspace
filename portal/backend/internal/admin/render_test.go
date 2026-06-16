package admin

import (
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// goldenServer is a representative deployed server exercising image, command,
// inline env, and a Vault KV secret ref.
func goldenServer() store.MCPServer {
	return store.MCPServer{
		Name:       "demo",
		Image:      "img:1",
		Command:    []string{"serve", "--http"},
		Env:        map[string]string{"LOG": "info"},
		SecretRefs: map[string]string{"API_KEY": "infra/mcp-servers/demo#key"},
		Transport:  "streamable-http",
		Port:       9100,
	}
}

func TestRenderMCPJobHCLStable(t *testing.T) {
	cfg := Config{MCPNamespace: "infra-mcp", NodePool: "agents", MCPJobVaultRole: "mcp-job"}.withDefaults()
	hcl := renderMCPJobHCL(goldenServer(), cfg)
	for _, want := range []string{
		`job "mcp-demo" {`,
		`namespace   = "infra-mcp"`,
		`node_pool   = "agents"`,
		`static = 9100`,
		`image      = "img:1"`,
		`"serve",`,
		`LOG = "info"`,
		`role = "mcp-job"`,
		`{{ with secret "infra/mcp-servers/demo" }}API_KEY={{ .Data.data.key }}{{ end }}`,
		`name     = "mcp-demo"`,
		`"mcp.transport=streamable-http",`,
		`"mcp.path=/mcp",`,
		`"mcp.managed-by=portal",`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("render missing %q in:\n%s", want, hcl)
		}
	}
}
