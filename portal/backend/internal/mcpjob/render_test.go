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

func TestRenderDynamicPort(t *testing.T) {
	s := baseSpec()
	s.DynamicPort = true
	hcl := Render(s)
	if strings.Contains(hcl, "static = 9100") {
		t.Fatalf("dynamic-port render must not pin a static host port:\n%s", hcl)
	}
	if !strings.Contains(hcl, "to     = 9100") {
		t.Fatalf("dynamic-port render must still map to the container port:\n%s", hcl)
	}
}

func TestRenderNoCredential(t *testing.T) {
	s := baseSpec()
	hcl := Render(s) // Credential nil → no vault stanza, no template
	if strings.Contains(hcl, "vault {") {
		t.Fatalf("no-credential render must not emit a vault stanza:\n%s", hcl)
	}
}
