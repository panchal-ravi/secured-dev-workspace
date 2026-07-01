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

// A Vault-authenticating server (no secret_refs) with InjectVaultToken set emits a
// bare vault{role} block so Nomad injects a WIF VAULT_TOKEN — and NO template.
func TestRenderMCPJobHCL_InjectVaultToken(t *testing.T) {
	cfg := Config{MCPNamespace: "infra-mcp", MCPJobVaultRole: "infra-mcp-selftest"}.withDefaults()
	srv := store.MCPServer{
		Name:             "vault-mcp",
		Image:            "hashicorp/vault-mcp-server:latest",
		Command:          []string{"http"},
		Transport:        "streamable-http",
		Port:             8080,
		InjectVaultToken: true,
	}
	hcl := renderMCPJobHCL(srv, cfg)
	if !strings.Contains(hcl, `role = "infra-mcp-selftest"`) {
		t.Fatalf("render missing bare vault role in:\n%s", hcl)
	}
	if strings.Contains(hcl, "template {") {
		t.Fatalf("InjectVaultToken should emit no template block:\n%s", hcl)
	}
}

// Without InjectVaultToken and without secret_refs, no vault block is emitted
// (mcp.auth=none, byte-identical to prior behavior).
func TestRenderMCPJobHCL_NoCredentialNoVaultBlock(t *testing.T) {
	cfg := Config{MCPNamespace: "infra-mcp", MCPJobVaultRole: "infra-mcp-selftest"}.withDefaults()
	srv := store.MCPServer{Name: "plain", Image: "img:1", Transport: "sse", Port: 9000}
	if hcl := renderMCPJobHCL(srv, cfg); strings.Contains(hcl, "vault {") {
		t.Fatalf("no-credential server should emit no vault block:\n%s", hcl)
	}
}
