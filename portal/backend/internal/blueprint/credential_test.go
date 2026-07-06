package blueprint

import (
	"errors"
	"reflect"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestCredentialSpec_Validate_RejectsBadShape(t *testing.T) {
	cases := []struct {
		name string
		spec CredentialSpec
	}{
		{"unknown source", CredentialSpec{Source: "magic"}},
		{"static without data", CredentialSpec{Source: SourceStatic, Static: &StaticSpec{}}},
		{"static with dynamic spec", CredentialSpec{Source: SourceStatic,
			Static: &StaticSpec{Data: map[string]string{"k": "v"}}, Dynamic: &DynamicSpec{}}},
		{"dynamic missing mount", CredentialSpec{Source: SourceDynamic,
			Dynamic: &DynamicSpec{Engine: "database", CredsPath: "creds/x"}}},
		{"dynamic missing creds path", CredentialSpec{Source: SourceDynamic,
			Dynamic: &DynamicSpec{Engine: "database", Mount: "database/x"}}},
		{"dynamic missing engine", CredentialSpec{Source: SourceDynamic,
			Dynamic: &DynamicSpec{Mount: "database/x", CredsPath: "creds/x"}}},
		{"none with static spec", CredentialSpec{Source: SourceNone,
			Static: &StaticSpec{Data: map[string]string{"k": "v"}}}},
		{"wif-token with dynamic spec", CredentialSpec{Source: SourceWIFToken, Dynamic: &DynamicSpec{}}},
		{"bad param type", CredentialSpec{Source: SourceNone,
			Params: []ParamSpec{{Name: "x", Type: "bytes"}}}},
		{"unnamed param", CredentialSpec{Source: SourceNone, Params: []ParamSpec{{Type: "string"}}}},
	}
	for _, c := range cases {
		if err := c.spec.Validate(); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("%s: want ErrBadRequest, got %v", c.name, err)
		}
	}
	// Escalating creds caps are forbidden, not just malformed.
	esc := CredentialSpec{Source: SourceDynamic, Dynamic: &DynamicSpec{
		Engine: "database", Mount: "database/x", CredsPath: "creds/x", CredsCaps: []string{"sudo"}}}
	if err := esc.Validate(); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("sudo creds cap: want ErrForbidden, got %v", err)
	}
	// And the good shapes pass.
	for _, ok := range []CredentialSpec{
		{Source: SourceNone},
		{Source: SourceWIFToken},
		staticSpec(),
		postgresSpec(),
		awsSpec(),
	} {
		if err := ok.Validate(); err != nil {
			t.Fatalf("valid spec rejected: %+v: %v", ok.Source, err)
		}
	}
}

func TestNonSecretParams_StripsSecrets(t *testing.T) {
	spec := postgresSpec()
	got := spec.NonSecretParams(pgParams())
	if _, ok := got["bootstrap_password"]; ok {
		t.Fatal("secret param must be stripped")
	}
	want := pgParams()
	delete(want, "bootstrap_password")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("non-secret params:\n got %v\nwant %v", got, want)
	}
}

// An unresolved ${param} anywhere in a write payload (including nested lists and
// maps) must error rather than write a literal "${...}" into Vault.
func TestInterpolate_UnresolvedParamFails(t *testing.T) {
	_, err := interpolateMap(map[string]any{
		"nested": map[string]any{"list": []any{"ok", "${missing}"}},
	}, map[string]string{"present": "x"})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("unresolved param: want ErrBadRequest, got %v", err)
	}
	// Resolved values substitute through nesting, non-strings pass through.
	got, err := interpolateMap(map[string]any{
		"url":  "postgres://${user}",
		"list": []any{"${user}", 42},
		"deep": map[string]any{"k": "${user}"},
		"flag": true,
	}, map[string]string{"user": "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if got["url"] != "postgres://alice" || got["flag"] != true {
		t.Fatalf("interpolated: %#v", got)
	}
	if l := got["list"].([]any); l[0] != "alice" || l[1] != 42 {
		t.Fatalf("list: %#v", l)
	}
	if d := got["deep"].(map[string]any); d["k"] != "alice" {
		t.Fatalf("deep: %#v", d)
	}
}

func TestLintWritePath_RejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "  ", "/abs/path", "config/../../../sys/mounts"} {
		if err := lintWritePath(bad); err == nil {
			t.Fatalf("path %q must be rejected", bad)
		}
	}
	// Traversal specifically is forbidden (not merely malformed).
	if err := lintWritePath("a/../b"); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("traversal: want ErrForbidden, got %v", err)
	}
	for _, good := range []string{"config/conn", "roles/mcp-ro", "config/rotate-root"} {
		if err := lintWritePath(good); err != nil {
			t.Fatalf("path %q must pass: %v", good, err)
		}
	}
	// ...and Validate wires it up for every dynamic path field.
	spec := postgresSpec()
	spec.Dynamic.Configs[0].Path = "../escape"
	if err := spec.Validate(); !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("config path traversal: want ErrForbidden, got %v", err)
	}
}

func TestDeriveCredentialPolicy(t *testing.T) {
	// Dynamic: exactly the creds path with the (default) read capability.
	pg := postgresSpec()
	if got := DeriveCredentialPolicy(pg, "secret", "postgres-mcp"); got != "path \"database/postgres-mcp/creds/mcp-ro\" {\n  capabilities = [\"read\"]\n}\n" {
		t.Fatalf("dynamic policy: %q", got)
	}
	// Static: read on the per-server mcp-secrets DATA path.
	if got := DeriveCredentialPolicy(staticSpec(), "secret", "everything-mcp"); got != "path \"secret/data/projects/mcp-secrets/everything-mcp\" {\n  capabilities = [\"read\"]\n}\n" {
		t.Fatalf("static policy: %q", got)
	}
	// None derives no policy at all.
	if got := DeriveCredentialPolicy(CredentialSpec{Source: SourceNone}, "secret", "x"); got != "" {
		t.Fatalf("none policy: %q", got)
	}
	// wif-token: projects/ subtree read PLUS the sys/mounts read baseline (the
	// token is the credential — vault-mcp-server probes sys/mounts for KV
	// version detection before every read).
	wifWant := "path \"secret/data/projects/*\" {\n  capabilities = [\"read\"]\n}\n" +
		"path \"sys/mounts\" {\n  capabilities = [\"read\"]\n}\n"
	if got := DeriveCredentialPolicy(CredentialSpec{Source: SourceWIFToken}, "secret", "vault-mcp"); got != wifWant {
		t.Fatalf("wif-token policy:\n got %q\nwant %q", got, wifWant)
	}
	// Custom creds caps flow through.
	sts := CredentialSpec{Source: SourceDynamic, Dynamic: &DynamicSpec{
		Engine: "aws", Mount: "aws/mcp", CredsPath: "sts/mcp", CredsCaps: []string{"read", "update"}}}
	if got := DeriveCredentialPolicy(sts, "secret", "aws-mcp"); got != "path \"aws/mcp/sts/mcp\" {\n  capabilities = [\"read\", \"update\"]\n}\n" {
		t.Fatalf("sts policy: %q", got)
	}
}
