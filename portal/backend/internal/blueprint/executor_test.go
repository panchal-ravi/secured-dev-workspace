package blueprint

import (
	"context"
	"strings"
	"testing"
)

// recVault is a VaultAdmin fake that records an ordered op log.
type recVault struct {
	ops      []string
	failRead bool
}

func (r *recVault) log(s string)                                            { r.ops = append(r.ops, s) }
func (r *recVault) CreateNamespace(_ context.Context, p string) error       { r.log("ns+ " + p); return nil }
func (r *recVault) DeleteNamespace(_ context.Context, p string) error       { r.log("ns- " + p); return nil }
func (r *recVault) MountEngine(_ context.Context, ns, p, t, v string) error { r.log("mount " + p); return nil }
func (r *recVault) UnmountEngine(_ context.Context, ns, p string) error     { r.log("unmount " + p); return nil }
func (r *recVault) ConfigureDBConnection(_ context.Context, ns, m, n string, c DBConnectionConfig) error {
	r.log("dbconfig " + m + "/" + n)
	return nil
}
func (r *recVault) RotateRoot(_ context.Context, ns, m, n string) error { r.log("rotate-root " + m + "/" + n); return nil }
func (r *recVault) WriteDBRole(_ context.Context, ns, m, n string, role DBRole) error {
	r.log("dbrole " + m + "/" + n)
	return nil
}
func (r *recVault) WriteKVv2(_ context.Context, ns, m, p string, d map[string]any) error {
	r.log("kv " + m + "/" + p)
	return nil
}
func (r *recVault) WritePolicy(_ context.Context, ns, n, h string) error { r.log("policy+ " + n); return nil }
func (r *recVault) DeletePolicy(_ context.Context, ns, n string) error   { r.log("policy- " + n); return nil }
func (r *recVault) WriteWIFRole(_ context.Context, ns, a, n string, role WIFRole) error {
	r.log("wif+ " + n)
	return nil
}
func (r *recVault) DeleteWIFRole(_ context.Context, ns, a, n string) error { r.log("wif- " + n); return nil }
func (r *recVault) RevokeLeasesByPrefix(_ context.Context, ns, p string) error {
	r.log("revoke " + p)
	return nil
}
func (r *recVault) MintTokenWithPolicies(_ context.Context, ns string, p []string, ttl string) (string, error) {
	r.log("mint")
	return "tok", nil
}
func (r *recVault) Read(_ context.Context, ns, tok, path string) (bool, error) {
	if strings.HasPrefix(path, "sys/") {
		return false, nil // a scoped token is always denied sys/ (clean 403)
	}
	return !r.failRead, nil
}

func classAManifest() BlueprintManifest {
	return BlueprintManifest{
		ID: "postgres-mcp", Version: 1, Class: ClassA, Description: "pg",
		Engines: []EngineSpec{{Type: "database", Plugin: "postgresql-database-plugin", MountPathTpl: "database/{{.Namespace}}-pg"}},
		Role:    &RoleSpec{NameTpl: "ro", CreationStatements: []string{"CREATE ROLE x;"}, DefaultTTLSeconds: 3600, MaxTTLSeconds: 7200},
		PolicyTpl: `path "{{.Mount}}/creds/{{.Role}}" { capabilities = ["read"] }`,
		WIFRole:   WIFRoleSpec{NameTpl: "mcp-postgres-mcp", TokenPolicies: []string{"mcp-postgres-mcp"}, TokenTTL: "1h"},
		Params: []ParamSpec{
			{Name: "connection_url", Type: "string", Required: true},
			{Name: "bootstrap_password", Type: "secret", Required: true},
		},
	}
}

func TestInstantiate_ClassA_OrderedAndRotatesRootBeforeRole(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), classAManifest(), "acme",
		map[string]string{"connection_url": "postgresql://...", "bootstrap_password": "boot"})
	if err != nil {
		t.Fatal(err)
	}
	order := strings.Join(rv.ops, ",")
	// mount → dbconfig → rotate-root → dbrole → policy → wif, in that order.
	for _, want := range []string{"mount", "dbconfig", "rotate-root", "dbrole", "policy+", "wif+"} {
		if !strings.Contains(order, want) {
			t.Fatalf("missing %q in %s", want, order)
		}
	}
	if idx(rv.ops, "rotate-root") < idx(rv.ops, "dbconfig") {
		t.Fatal("rotate-root must come AFTER dbconfig")
	}
	if idx(rv.ops, "rotate-root") > idx(rv.ops, "dbrole") {
		t.Fatal("rotate-root must come BEFORE writing the role")
	}
	if len(rec.LeasePrefixes) != 1 || !strings.Contains(rec.LeasePrefixes[0], "/creds/ro") {
		t.Fatalf("class A must record a creds lease prefix, got %v", rec.LeasePrefixes)
	}
}

func TestDeprovision_RevokesLeasesBeforeUnmount(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, _ := ex.Instantiate(context.Background(), classAManifest(), "acme",
		map[string]string{"connection_url": "x", "bootstrap_password": "boot"})
	rv.ops = nil
	if err := ex.Deprovision(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if idx(rv.ops, "revoke") > idx(rv.ops, "unmount") {
		t.Fatalf("revoke must precede unmount: %v", rv.ops)
	}
}

func TestInstantiate_ClassC_NoEngineNoSecret(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	_, err := ex.Instantiate(context.Background(), validClassC(), "acme", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mount", "dbconfig", "rotate-root", "kv"} {
		if contains(rv.ops, forbidden) {
			t.Fatalf("class C must not %q: %v", forbidden, rv.ops)
		}
	}
}

func idx(ops []string, sub string) int {
	for i, o := range ops {
		if strings.HasPrefix(o, sub) {
			return i
		}
	}
	return 1 << 30
}
func contains(ops []string, sub string) bool { return idx(ops, sub) < (1 << 30) }
