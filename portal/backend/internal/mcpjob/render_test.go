package mcpjob

import (
	"strings"
	"testing"
)

func baseSpec() RenderSpec {
	return RenderSpec{
		JobName:     "mcp-demo",
		Namespace:   "infra-mcp",
		Image:       "img:1",
		Port:        9100,
		ServiceName: "mcp-demo",
		Tags:        []string{"mcp", "mcp.server=demo"},
	}
}

func TestRenderKVCredential(t *testing.T) {
	s := baseSpec()
	s.Credential = KVCredential{VaultRole: "mcp-job", SecretRefs: map[string]string{"API_KEY": "infra/mcp-servers/demo#key"}}
	hcl := Render(s)
	for _, want := range []string{
		`job "mcp-demo" {`,
		`namespace   = "infra-mcp"`,
		`static = 9100`,
		`image      = "img:1"`,
		`vault {`,
		`role = "mcp-job"`,
		`{{ with secret "infra/mcp-servers/demo" }}API_KEY={{ .Data.data.key }}{{ end }}`,
		`"mcp.server=demo",`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("KV render missing %q in:\n%s", want, hcl)
		}
	}
	if strings.Contains(hcl, "namespace =") && strings.Contains(hcl, "vault {\n        namespace") {
		t.Fatalf("KV render must not emit a vault namespace")
	}
}

func TestRenderWIFCredential(t *testing.T) {
	s := baseSpec()
	s.Credential = WIFCredential{
		VaultNamespace: "project-acme",
		WIFRole:        "mcp-postgres-mcp",
		EnvTemplates:   map[string]string{"DATABASE_URI": `{{ with secret "database/project-acme-pg/creds/mcp-ro" }}postgresql://x{{ end }}`},
	}
	hcl := Render(s)
	for _, want := range []string{
		`vault {`,
		`namespace = "project-acme"`,
		`role      = "mcp-postgres-mcp"`,
		`DATABASE_URI={{ with secret "database/project-acme-pg/creds/mcp-ro" }}postgresql://x{{ end }}`,
	} {
		if !strings.Contains(hcl, want) {
			t.Fatalf("WIF render missing %q in:\n%s", want, hcl)
		}
	}
}

func TestRenderNoCredential(t *testing.T) {
	s := baseSpec()
	hcl := Render(s) // Credential nil → no vault stanza, no template
	if strings.Contains(hcl, "vault {") {
		t.Fatalf("no-credential render must not emit a vault stanza:\n%s", hcl)
	}
}
