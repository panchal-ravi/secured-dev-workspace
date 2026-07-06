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
		"sys":              `path "sys/policies/acl/x" { capabilities = ["read"] }`,
		"sys-mounts-write": `path "sys/mounts" { capabilities = ["read","update"] }`,
		"sys-mounts-sub":   `path "sys/mounts/secret" { capabilities = ["read"] }`,
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

// LintPolicy shares the sys/mounts carve-out with RenderGrants: the derived
// wif-token baseline carries "sys/mounts read" and must pass the lint even though
// sys/ is otherwise denied and the path is outside the mount allowlist.
func TestLintPolicy_SysMountsReadOnlyCarveOut(t *testing.T) {
	p := `path "secret/data/projects/x" { capabilities = ["read"] }
path "sys/mounts" { capabilities = ["read"] }`
	if err := LintPolicy(p, []string{"secret/data/projects/"}); err != nil {
		t.Fatalf("sys/mounts read must pass the lint: %v", err)
	}
}

func TestAllowedPrefixes_KVMountNotBarePrefix(t *testing.T) {
	// Class B/C: mount == kvMount. The bare "secret/" must NOT be allowed — only the
	// projects/ subtree — so a policy over the whole KV mount fails.
	prefixes := allowedPrefixes("secret", "secret")
	for _, p := range prefixes {
		if p == "secret/" {
			t.Fatalf("bare KV mount root must not be an allowed prefix: %v", prefixes)
		}
	}
	wholeMount := `path "secret/data/other-blueprint" { capabilities = ["read"] }`
	if err := LintPolicy(wholeMount, prefixes); err == nil {
		t.Fatal("a Class B/C policy over the whole KV mount must be rejected")
	}
	ownSlice := `path "secret/data/projects/my-id" { capabilities = ["read"] }`
	if err := LintPolicy(ownSlice, prefixes); err != nil {
		t.Fatalf("a policy within the namespace's own KV slice must pass: %v", err)
	}

	// Class A: a dedicated engine mount IS allowed at its own subtree.
	aPrefixes := allowedPrefixes("secret", "database/acme-pg")
	creds := `path "database/acme-pg/creds/ro" { capabilities = ["read"] }`
	if err := LintPolicy(creds, aPrefixes); err != nil {
		t.Fatalf("class A db-creds policy must pass: %v", err)
	}
}

func TestRenderGrants_AcceptsNamespaceLocalWrite(t *testing.T) {
	out, err := RenderGrants([]PathGrant{
		{Path: "pki/acme/issue/web", Capabilities: []string{"create", "update"}},
		{Path: "/secret/data/shared", Capabilities: []string{"read", "list"}}, // leading slash trimmed
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
	if !strings.Contains(out, `path "pki/acme/issue/web"`) || !strings.Contains(out, `"create", "update"`) {
		t.Fatalf("grant block not rendered: %s", out)
	}
	if !strings.Contains(out, `path "secret/data/shared"`) {
		t.Fatalf("leading slash not normalized: %s", out)
	}
}

// The one sys/ carve-out: exact sys/mounts, read only. Namespace-relative (a
// project token sees only its own mounts) and required by vault-mcp-server's KV
// version detection. Anything beyond exact-path + read stays denied.
func TestRenderGrants_SysMountsReadOnlyCarveOut(t *testing.T) {
	out, err := RenderGrants([]PathGrant{{Path: "sys/mounts", Capabilities: []string{"read"}}})
	if err != nil {
		t.Fatalf("sys/mounts read must be grantable: %v", err)
	}
	if !strings.Contains(out, `path "sys/mounts"`) || !strings.Contains(out, `"read"`) {
		t.Fatalf("carve-out not rendered: %s", out)
	}
}

func TestRenderGrants_Rejects(t *testing.T) {
	cases := map[string]PathGrant{
		"sys":                {Path: "sys/policies/acl/x", Capabilities: []string{"read"}},
		"sys-mounts-list":    {Path: "sys/mounts", Capabilities: []string{"read", "list"}},
		"sys-mounts-write":   {Path: "sys/mounts", Capabilities: []string{"update"}},
		"sys-mounts-subpath": {Path: "sys/mounts/secret", Capabilities: []string{"read"}},
		"sys-mounts-glob":    {Path: "sys/mounts*", Capabilities: []string{"read"}},
		"sys-mounts-no-caps": {Path: "sys/mounts", Capabilities: nil},
		"auth":               {Path: "auth/token/create", Capabilities: []string{"create"}},
		"identity":           {Path: "identity/entity", Capabilities: []string{"read"}},
		"cubbyhole":          {Path: "cubbyhole/x", Capabilities: []string{"read"}},
		"traversal":          {Path: "secret/../sys/mounts", Capabilities: []string{"read"}},
		"root-glob":          {Path: "*", Capabilities: []string{"read"}},
		"sudo-cap":           {Path: "pki/acme/issue/web", Capabilities: []string{"read", "sudo"}},
		"bogus-cap":          {Path: "pki/acme/issue/web", Capabilities: []string{"frobnicate"}},
		"empty-path":         {Path: "  ", Capabilities: []string{"read"}},
		"no-caps":            {Path: "pki/acme/issue/web", Capabilities: nil},
	}
	for name, g := range cases {
		if _, err := RenderGrants([]PathGrant{g}); err == nil {
			t.Errorf("%s: expected rejection, got nil", name)
		}
	}
}
