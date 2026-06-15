package blueprint

import (
	"strings"
	"testing"
)

func TestRenderPolicy_SubstitutesPlaceholders(t *testing.T) {
	tpl := `path "{{.Mount}}/creds/{{.Role}}" { capabilities = ["read"] }`
	got, err := RenderPolicy(tpl, PolicyVars{Namespace: "acme", Mount: "database/acme", Role: "ro"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `path "database/acme/creds/ro"`) {
		t.Fatalf("placeholders not substituted: %s", got)
	}
}

func TestLintPolicy_AcceptsNamespaceLocalRead(t *testing.T) {
	p := `path "database/acme/creds/ro" { capabilities = ["read"] }`
	if err := LintPolicy(p, []string{"database/acme/"}); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestLintPolicy_RejectsEscapes(t *testing.T) {
	cases := map[string]string{
		"sys":              `path "sys/mounts" { capabilities = ["read"] }`,
		"auth":             `path "auth/token/create" { capabilities = ["create","update"] }`,
		"identity":         `path "identity/entity" { capabilities = ["read"] }`,
		"cubbyhole":        `path "cubbyhole/x" { capabilities = ["read"] }`,
		"sudo":             `path "database/acme/creds/ro" { capabilities = ["read","sudo"] }`,
		"other-mount":      `path "database/other/creds/ro" { capabilities = ["read"] }`,
		"glob-escape":      `path "*" { capabilities = ["read"] }`,
		"malformed":        `path "database/acme/creds/ro" { capabilities = [`,
		"dotdot-traversal": `path "database/acme/../sys/mounts" { capabilities = ["read"] }`,
	}
	for name, p := range cases {
		if err := LintPolicy(p, []string{"database/acme/"}); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}
