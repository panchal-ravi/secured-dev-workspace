package blueprint

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// recVault is a VaultAdmin fake that records an ordered op log.
type recVault struct {
	ops        []string
	lastPolicy string
}

func (r *recVault) log(s string) { r.ops = append(r.ops, s) }
func (r *recVault) MountEngine(_ context.Context, ns, p, t, v string) error {
	r.log("mount " + p)
	return nil
}
func (r *recVault) UnmountEngine(_ context.Context, ns, p string) error {
	r.log("unmount " + p)
	return nil
}
func (r *recVault) ConfigureDBConnection(_ context.Context, ns, m, n string, c DBConnectionConfig) error {
	r.log("dbconfig " + m + "/" + n)
	return nil
}
func (r *recVault) RotateRoot(_ context.Context, ns, m, n string) error {
	r.log("rotate-root " + m + "/" + n)
	return nil
}
func (r *recVault) WriteDBRole(_ context.Context, ns, m, n string, role DBRole) error {
	r.log("dbrole " + m + "/" + n)
	return nil
}
func (r *recVault) WriteKVv2(_ context.Context, ns, m, p string, d map[string]any) error {
	r.log("kv " + m + "/" + p)
	return nil
}
func (r *recVault) WritePolicy(_ context.Context, ns, n, h string) error {
	r.log("policy+ " + n)
	r.lastPolicy = h
	return nil
}
func (r *recVault) DeletePolicy(_ context.Context, ns, n string) error {
	r.log("policy- " + n)
	return nil
}
func (r *recVault) WriteWIFRole(_ context.Context, ns, a, n string, role WIFRole) error {
	r.log("wif+ " + n)
	return nil
}
func (r *recVault) DeleteWIFRole(_ context.Context, ns, a, n string) error {
	r.log("wif- " + n)
	return nil
}
func (r *recVault) RevokeLeasesByPrefix(_ context.Context, ns, p string) error {
	r.log("revoke " + p)
	return nil
}
func (r *recVault) WriteSSHCA(_ context.Context, ns, m string) error {
	r.log("ssh-ca " + m)
	return nil
}
func (r *recVault) WriteSSHRole(_ context.Context, ns, m, n string, role SSHRole) error {
	r.log("ssh-role " + m + "/" + n)
	return nil
}
func (r *recVault) WriteGitHubConfig(_ context.Context, ns, m string, appID int, pem string) error {
	r.log("gh-config " + m)
	return nil
}
func (r *recVault) WriteGitHubPermissionSet(_ context.Context, ns, m, n string, id int, perms map[string]string, repos []string) error {
	r.log("gh-permset " + m + "/" + n)
	return nil
}
func (r *recVault) CreatePeriodicToken(_ context.Context, ns string, policies []string, period string) (string, error) {
	r.log("periodic-token")
	return "periodic-tok", nil
}

func classAManifest() BlueprintManifest {
	return BlueprintManifest{
		ID: "postgres-mcp", Version: 1, Class: ClassA, Description: "pg",
		Engines:   []EngineSpec{{Type: "database", Plugin: "postgresql-database-plugin", MountPathTpl: "database/{{.Namespace}}-pg"}},
		Role:      &RoleSpec{NameTpl: "ro", CreationStatements: []string{"CREATE ROLE x;"}, DefaultTTLSeconds: 3600, MaxTTLSeconds: 7200},
		PolicyTpl: `path "{{.Mount}}/creds/{{.Role}}" { capabilities = ["read"] }`,
		WIFRole:   WIFRoleSpec{NameTpl: "mcp-postgres-mcp", TokenTTL: "1h"},
		Params: []ParamSpec{
			{Name: "connection_url", Type: "string", Required: true},
			{Name: "bootstrap_password", Type: "secret", Required: true},
		},
		JobCredential: JobCredentialSpec{EnvTemplates: map[string]string{
			"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}/${db_name}{{ end }}`,
		}},
	}
}

func TestInstantiate_ClassA_OrderedAndRotatesRootBeforeRole(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), classAManifest(), "acme",
		map[string]string{"connection_url": "postgresql://...", "bootstrap_password": "boot"}, nil)
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
		map[string]string{"connection_url": "x", "bootstrap_password": "boot"}, nil)
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
	_, err := ex.Instantiate(context.Background(), validClassC(), "acme", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mount", "dbconfig", "rotate-root", "kv"} {
		if contains(rv.ops, forbidden) {
			t.Fatalf("class C must not %q: %v", forbidden, rv.ops)
		}
	}
}

func TestInstantiate_RejectsEmptyRequiredParam(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	// A required secret present-but-empty must be rejected before any Vault write.
	_, err := ex.Instantiate(context.Background(), classAManifest(), "acme",
		map[string]string{"connection_url": "postgresql://...", "bootstrap_password": ""}, nil)
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty required param: want ErrBadRequest, got %v", err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("no Vault ops should run when a required param is empty: %v", rv.ops)
	}
}

func TestInstantiate_ExtraGrants_RequireOptIn(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	// validClassC() does NOT set AllowExtraGrants, so any grant must be refused...
	grants := []PathGrant{{Path: "pki/acme/issue/web", Capabilities: []string{"create", "update"}}}
	_, err := ex.Instantiate(context.Background(), validClassC(), "acme", nil, grants)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("grants without opt-in: want ErrForbidden, got %v", err)
	}
	// ...and refused before any Vault side effect.
	if len(rv.ops) != 0 {
		t.Fatalf("no Vault ops should run when grants are refused: %v", rv.ops)
	}
}

func TestInstantiate_ExtraGrants_AppendedWhenAllowed(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	m := validClassC()
	m.AllowExtraGrants = true
	grants := []PathGrant{{Path: "pki/acme/issue/web", Capabilities: []string{"create", "update"}}}
	rec, err := ex.Instantiate(context.Background(), m, "acme", nil, grants)
	if err != nil {
		t.Fatal(err)
	}
	// The written policy keeps the base path AND carries the appended grant block.
	if !strings.Contains(rv.lastPolicy, "data/projects/") {
		t.Fatalf("base policy path missing: %s", rv.lastPolicy)
	}
	if !strings.Contains(rv.lastPolicy, `path "pki/acme/issue/web"`) ||
		!strings.Contains(rv.lastPolicy, `"create"`) {
		t.Fatalf("grant block missing from policy: %s", rv.lastPolicy)
	}
	if len(rec.ExtraGrants) != 1 {
		t.Fatalf("instance record must capture grants, got %v", rec.ExtraGrants)
	}
}

func TestInstantiate_ExtraGrants_ForbiddenPathFailsBeforeSideEffects(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	// Class A mounts an engine mid-flow; a forbidden grant must fail before that.
	m := classAManifest()
	m.AllowExtraGrants = true
	grants := []PathGrant{{Path: "sys/mounts", Capabilities: []string{"read"}}}
	_, err := ex.Instantiate(context.Background(), m, "acme",
		map[string]string{"connection_url": "x", "bootstrap_password": "boot"}, grants)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("forbidden grant: want ErrForbidden, got %v", err)
	}
	if contains(rv.ops, "mount") {
		t.Fatalf("a forbidden grant must fail before MountEngine: %v", rv.ops)
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
