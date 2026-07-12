package blueprint

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// recVault is a VaultAdmin fake that records an ordered op log.
type recVault struct {
	ops        []string
	writes     map[string]map[string]any // logical path -> payload
	lastPolicy string
}

func (r *recVault) log(s string) { r.ops = append(r.ops, s) }
func (r *recVault) MountEngine(_ context.Context, ns, p, t, v string) error {
	r.log("mount " + t + " " + p)
	return nil
}
func (r *recVault) UnmountEngine(_ context.Context, ns, p string) error {
	r.log("unmount " + p)
	return nil
}
func (r *recVault) WriteLogical(_ context.Context, ns, p string, d map[string]any) error {
	r.log("write " + p)
	if r.writes == nil {
		r.writes = map[string]map[string]any{}
	}
	r.writes[p] = d
	return nil
}
func (r *recVault) DeleteKVv2Metadata(_ context.Context, ns, m, p string) error {
	r.log("kv-delete " + m + "/" + p)
	return nil
}
func (r *recVault) WriteKVv2(_ context.Context, ns, m, p string, d map[string]any) error {
	r.log("kv " + m + "/" + p)
	if r.writes == nil {
		r.writes = map[string]map[string]any{}
	}
	r.writes["kv:"+m+"/"+p] = d
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

// postgresSpec is a Go copy of the frontend PostgreSQL preset (mcpPresets.ts) —
// the parity anchor with the retired Class-A recipe.
func postgresSpec() CredentialSpec {
	return CredentialSpec{
		Source: SourceDynamic,
		Params: []ParamSpec{
			{Name: "connection_url", Type: "string", Required: true},
			{Name: "bootstrap_username", Type: "string", Required: true},
			{Name: "bootstrap_password", Type: "secret", Required: true},
			{Name: "db_host", Type: "string", Required: true},
			{Name: "db_port", Type: "string", Required: true},
			{Name: "db_name", Type: "string", Required: true},
		},
		Dynamic: &DynamicSpec{
			Engine: "database",
			Mount:  "database/postgres-mcp",
			Configs: []LogicalWrite{{Path: "config/conn", Data: map[string]any{
				"plugin_name":       "postgresql-database-plugin",
				"connection_url":    "${connection_url}",
				"username":          "${bootstrap_username}",
				"password":          "${bootstrap_password}",
				"allowed_roles":     []string{"mcp-ro"},
				"verify_connection": false,
			}}},
			RotateRootPath: "rotate-root/conn",
			Role: &LogicalWrite{Path: "roles/mcp-ro", Data: map[string]any{
				"db_name": "conn",
				"creation_statements": []string{
					`CREATE ROLE "{{name}}" WITH LOGIN PASSWORD '{{password}}' VALID UNTIL '{{expiration}}';`,
					`GRANT USAGE ON SCHEMA public TO "{{name}}";`,
					`GRANT SELECT ON ALL TABLES IN SCHEMA public TO "{{name}}";`,
				},
				"default_ttl": 3600,
				"max_ttl":     86400,
			}},
			CredsPath: "creds/mcp-ro",
		},
		EnvTemplates: map[string]string{
			"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}`,
		},
	}
}

func pgParams() map[string]string {
	return map[string]string{
		"connection_url":     "postgresql://{{username}}:{{password}}@10.0.0.1:15432/appdb?sslmode=disable",
		"bootstrap_username": "vaultadmin",
		"bootstrap_password": "boot-secret",
		"db_host":            "10.0.0.1",
		"db_port":            "15432",
		"db_name":            "appdb",
	}
}

// The generic executor driven by the PostgreSQL preset must reproduce the retired
// Class-A recipe: mount database → config (with the bootstrap creds + the
// verify_connection:false the old ConfigureDBConnection hardcoded) → rotate-root
// (AFTER config, BEFORE the role) → role → policy → WIF role.
func TestInstantiate_DynamicPostgresPreset_MatchesLegacyClassAWrites(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "postgres-mcp", postgresSpec(), "acme", pgParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mount database database/postgres-mcp", "write database/postgres-mcp/config/conn",
		"write database/postgres-mcp/rotate-root/conn", "write database/postgres-mcp/roles/mcp-ro",
		"policy+ mcp-postgres-mcp", "wif+ mcp-postgres-mcp"} {
		if !contains(rv.ops, want) {
			t.Fatalf("missing %q in %v", want, rv.ops)
		}
	}
	if idx(rv.ops, "write database/postgres-mcp/rotate-root") < idx(rv.ops, "write database/postgres-mcp/config") {
		t.Fatal("rotate-root must come AFTER the connection config")
	}
	if idx(rv.ops, "write database/postgres-mcp/rotate-root") > idx(rv.ops, "write database/postgres-mcp/roles") {
		t.Fatal("rotate-root must come BEFORE writing the role")
	}
	// Config payload parity with the old ConfigureDBConnection.
	cfg := rv.writes["database/postgres-mcp/config/conn"]
	wantCfg := map[string]any{
		"plugin_name":       "postgresql-database-plugin",
		"connection_url":    "postgresql://{{username}}:{{password}}@10.0.0.1:15432/appdb?sslmode=disable",
		"username":          "vaultadmin",
		"password":          "boot-secret",
		"allowed_roles":     []any{"mcp-ro"},
		"verify_connection": false,
	}
	if !reflect.DeepEqual(cfg, wantCfg) {
		t.Fatalf("config payload mismatch:\n got %#v\nwant %#v", cfg, wantCfg)
	}
	// Role payload parity with the old WriteDBRole (numbers compared semantically —
	// a JSON round-trip may deliver float64).
	role := rv.writes["database/postgres-mcp/roles/mcp-ro"]
	if role["db_name"] != "conn" {
		t.Fatalf("role db_name: %#v", role["db_name"])
	}
	if n := numeric(role["default_ttl"]); n != 3600 {
		t.Fatalf("role default_ttl: %v", role["default_ttl"])
	}
	if n := numeric(role["max_ttl"]); n != 86400 {
		t.Fatalf("role max_ttl: %v", role["max_ttl"])
	}
	stmts, _ := role["creation_statements"].([]any)
	if len(stmts) != 3 || !strings.Contains(stmts[0].(string), "CREATE ROLE") {
		t.Fatalf("creation_statements: %#v", role["creation_statements"])
	}
	// Teardown record parity.
	if rec.CredPath != "database/postgres-mcp/creds/mcp-ro" {
		t.Fatalf("CredPath: %q", rec.CredPath)
	}
	if len(rec.LeasePrefixes) != 1 || rec.LeasePrefixes[0] != rec.CredPath {
		t.Fatalf("lease prefix must equal the creds path: %+v", rec)
	}
	if !reflect.DeepEqual(rec.Mounts, []string{"database/postgres-mcp"}) {
		t.Fatalf("Mounts: %v", rec.Mounts)
	}
	if rec.WIFRoleName != "mcp-postgres-mcp" || rec.PolicyNames[0] != "mcp-postgres-mcp" {
		t.Fatalf("naming: %+v", rec)
	}
	// Derived policy grants exactly the creds path, read-only.
	if !strings.Contains(rv.lastPolicy, `path "database/postgres-mcp/creds/mcp-ro"`) ||
		!strings.Contains(rv.lastPolicy, `"read"`) {
		t.Fatalf("policy: %s", rv.lastPolicy)
	}
}

func awsSpec() CredentialSpec {
	return CredentialSpec{
		Source: SourceDynamic,
		Params: []ParamSpec{
			{Name: "access_key", Type: "string", Required: true},
			{Name: "secret_key", Type: "secret", Required: true},
			{Name: "region", Type: "string", Required: true},
			{Name: "policy_document", Type: "string", Required: true},
		},
		Dynamic: &DynamicSpec{
			Engine: "aws",
			Mount:  "aws/mcp",
			Configs: []LogicalWrite{{Path: "config/root", Data: map[string]any{
				"access_key": "${access_key}",
				"secret_key": "${secret_key}",
				"region":     "${region}",
			}}},
			RotateRootPath: "config/rotate-root",
			Role: &LogicalWrite{Path: "roles/mcp", Data: map[string]any{
				"credential_type": "iam_user",
				"policy_document": "${policy_document}",
			}},
			CredsPath: "creds/mcp",
		},
		EnvTemplates: map[string]string{
			"AWS_ACCESS_KEY_ID":     `{{ with secret "${cred_path}" }}{{ .Data.access_key }}{{ end }}`,
			"AWS_SECRET_ACCESS_KEY": `{{ with secret "${cred_path}" }}{{ .Data.secret_key }}{{ end }}`,
		},
	}
}

// The same generic path must carry a completely different engine (AWS): only the
// preset differs, no engine-specific executor code.
func TestInstantiate_DynamicAWSPreset_Writes(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	params := map[string]string{
		"access_key": "AKIA123", "secret_key": "sk-abc", "region": "ap-southeast-1",
		"policy_document": `{"Version":"2012-10-17"}`,
	}
	rec, err := ex.Instantiate(context.Background(), "aws-mcp", awsSpec(), "acme", params, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mount aws aws/mcp", "write aws/mcp/config/root",
		"write aws/mcp/config/rotate-root", "write aws/mcp/roles/mcp", "policy+ mcp-aws-mcp", "wif+ mcp-aws-mcp"} {
		if !contains(rv.ops, want) {
			t.Fatalf("missing %q in %v", want, rv.ops)
		}
	}
	root := rv.writes["aws/mcp/config/root"]
	if root["access_key"] != "AKIA123" || root["secret_key"] != "sk-abc" || root["region"] != "ap-southeast-1" {
		t.Fatalf("root config: %#v", root)
	}
	if rec.CredPath != "aws/mcp/creds/mcp" || len(rec.LeasePrefixes) != 1 {
		t.Fatalf("record: %+v", rec)
	}
}

func staticSpec() CredentialSpec {
	return CredentialSpec{
		Source: SourceStatic,
		Params: []ParamSpec{{Name: "api_key", Type: "secret", Required: true}},
		Static: &StaticSpec{Data: map[string]string{"api_key": "${api_key}"}},
		EnvTemplates: map[string]string{
			"API_KEY": `{{ with secret "${cred_path}" }}{{ .Data.data.api_key }}{{ end }}`,
		},
	}
}

// The static source seeds the write-only secret into the project KV under a
// per-server path and records the KV v2 DATA path as ${cred_path}.
func TestInstantiate_Static_SeedsKVAndDataCredPath(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "everything-mcp", staticSpec(), "acme",
		map[string]string{"api_key": "sk-xyz"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(rv.ops, "kv secret/projects/mcp-secrets/everything-mcp") {
		t.Fatalf("static must seed the KV secret: %v", rv.ops)
	}
	if got := rv.writes["kv:secret/projects/mcp-secrets/everything-mcp"]; got["api_key"] != "sk-xyz" {
		t.Fatalf("seeded data: %#v", got)
	}
	for _, forbidden := range []string{"mount ", "write "} {
		if contains(rv.ops, forbidden) {
			t.Fatalf("static must not %q: %v", forbidden, rv.ops)
		}
	}
	if rec.CredPath != "secret/data/projects/mcp-secrets/everything-mcp" {
		t.Fatalf("CredPath must be the KV v2 DATA path, got %q", rec.CredPath)
	}
	if len(rec.LeasePrefixes) != 0 || len(rec.Mounts) != 0 {
		t.Fatalf("static mints no leases and mounts nothing: %+v", rec)
	}
}

// The wif-token source derives the same policy the retired classC seed shipped:
// read over the whole projects/ KV subtree.
func TestInstantiate_WIFToken_PolicyOverProjectsSubtree(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "vault-mcp",
		CredentialSpec{Source: SourceWIFToken}, "acme", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "path \"secret/data/projects/*\" {\n  capabilities = [\"read\"]\n}\n" +
		"path \"sys/mounts\" {\n  capabilities = [\"read\"]\n}\n"
	if rv.lastPolicy != want {
		t.Fatalf("policy:\n got %q\nwant %q", rv.lastPolicy, want)
	}
	for _, forbidden := range []string{"mount", "write", "kv"} {
		if contains(rv.ops, forbidden) {
			t.Fatalf("wif-token must not %q: %v", forbidden, rv.ops)
		}
	}
	if rec.WIFRoleName != "mcp-vault-mcp" || rec.CredPath != "" {
		t.Fatalf("record: %+v", rec)
	}
}

// source=none is a no-op: nothing in Vault, nothing to tear down.
func TestInstantiate_None_NoVaultOps(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "plain-mcp",
		CredentialSpec{Source: SourceNone}, "acme", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("none must perform no Vault ops: %v", rv.ops)
	}
	if rec.WIFRoleName != "" || len(rec.PolicyNames) != 0 {
		t.Fatalf("none must leave nothing to tear down: %+v", rec)
	}
	// ...and grants are meaningless without a WIF token to carry them.
	_, err = ex.Instantiate(context.Background(), "plain-mcp", CredentialSpec{Source: SourceNone}, "acme", nil,
		[]PathGrant{{Path: "pki/issue/web", Capabilities: []string{"read"}}})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("grants with none: want ErrBadRequest, got %v", err)
	}
}

func TestDeprovision_RevokesLeasesBeforeUnmount(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "postgres-mcp", postgresSpec(), "acme", pgParams(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rv.ops = nil
	if err := ex.Deprovision(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if idx(rv.ops, "revoke") > idx(rv.ops, "unmount") {
		t.Fatalf("revoke must precede unmount: %v", rv.ops)
	}
}

// Deprovisioning a static instance deletes the seeded KV secret (metadata + all
// versions) — the retired Class B left it orphaned.
func TestDeprovision_DeletesStaticKVSecret(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec, err := ex.Instantiate(context.Background(), "everything-mcp", staticSpec(), "acme",
		map[string]string{"api_key": "sk-xyz"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rv.ops = nil
	if err := ex.Deprovision(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if !contains(rv.ops, "kv-delete secret/projects/mcp-secrets/everything-mcp") {
		t.Fatalf("static deprovision must delete the KV secret: %v", rv.ops)
	}
}

func TestInstantiate_RejectsEmptyRequiredParam(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	p := pgParams()
	p["bootstrap_password"] = ""
	_, err := ex.Instantiate(context.Background(), "postgres-mcp", postgresSpec(), "acme", p, nil)
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty required param: want ErrBadRequest, got %v", err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("no Vault ops should run when a required param is empty: %v", rv.ops)
	}
}

// Grants are appended to the derived policy (no opt-in gate anymore — the
// project-admin authors the whole spec).
func TestInstantiate_ExtraGrants_Appended(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	grants := []PathGrant{{Path: "pki/acme/issue/web", Capabilities: []string{"create", "update"}}}
	rec, err := ex.Instantiate(context.Background(), "vault-mcp",
		CredentialSpec{Source: SourceWIFToken}, "acme", nil, grants)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rv.lastPolicy, "data/projects/") {
		t.Fatalf("base policy path missing: %s", rv.lastPolicy)
	}
	if !strings.Contains(rv.lastPolicy, `path "pki/acme/issue/web"`) || !strings.Contains(rv.lastPolicy, `"create"`) {
		t.Fatalf("grant block missing from policy: %s", rv.lastPolicy)
	}
	if len(rec.ExtraGrants) != 1 {
		t.Fatalf("instance record must capture grants, got %v", rec.ExtraGrants)
	}
}

// A forbidden grant must fail before ANY Vault side effect (the dynamic source
// mounts an engine mid-flow).
func TestInstantiate_GrantsForbiddenPathFailsBeforeSideEffects(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	grants := []PathGrant{{Path: "sys/policies/acl/x", Capabilities: []string{"read"}}}
	_, err := ex.Instantiate(context.Background(), "postgres-mcp", postgresSpec(), "acme", pgParams(), grants)
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("forbidden grant: want ErrForbidden, got %v", err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("a forbidden grant must fail before any Vault op: %v", rv.ops)
	}
}

// UpdateGrants must rewrite the SAME policy name in place: derived base +
// replacement grants, with the record's ExtraGrants updated to match. An empty
// grant set reverts to the derived policy alone.
func TestUpdateGrants_RewritesPolicyInPlace(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	spec := CredentialSpec{Source: SourceWIFToken}
	rec, err := ex.Instantiate(context.Background(), "vault-mcp", spec, "acme", nil,
		[]PathGrant{{Path: "secret/data/projects/old", Capabilities: []string{"read"}}})
	if err != nil {
		t.Fatal(err)
	}

	rec, err = ex.UpdateGrants(context.Background(), rec, spec, "vault-mcp",
		[]PathGrant{{Path: "pki/acme/issue/web", Capabilities: []string{"create"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := rv.ops[len(rv.ops)-1]; got != "policy+ mcp-vault-mcp" {
		t.Fatalf("must rewrite the same policy name, last op %q", got)
	}
	if !strings.Contains(rv.lastPolicy, "data/projects/") || !strings.Contains(rv.lastPolicy, `path "pki/acme/issue/web"`) {
		t.Fatalf("rewritten policy must carry base + new grant: %s", rv.lastPolicy)
	}
	if strings.Contains(rv.lastPolicy, "projects/old") {
		t.Fatalf("old grant must be replaced, not appended: %s", rv.lastPolicy)
	}
	if len(rec.ExtraGrants) != 1 || rec.ExtraGrants[0].Path != "pki/acme/issue/web" {
		t.Fatalf("record must capture the new grants, got %v", rec.ExtraGrants)
	}

	rec, err = ex.UpdateGrants(context.Background(), rec, spec, "vault-mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rv.lastPolicy, "pki/acme") || len(rec.ExtraGrants) != 0 {
		t.Fatalf("empty set must revert to the derived policy alone: %s / %v", rv.lastPolicy, rec.ExtraGrants)
	}
}

func TestUpdateGrants_ForbiddenPathFailsBeforeWrite(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	rec := InstanceRecord{Namespace: "acme", PolicyNames: []string{"mcp-vault-mcp"}}
	_, err := ex.UpdateGrants(context.Background(), rec, CredentialSpec{Source: SourceWIFToken}, "vault-mcp",
		[]PathGrant{{Path: "sys/policies/acl/x", Capabilities: []string{"read"}}})
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("forbidden grant: want ErrForbidden, got %v", err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("a forbidden grant must fail before any Vault op: %v", rv.ops)
	}
}

// A source=none deploy has no policy or WIF token — nothing to carry grants.
func TestUpdateGrants_NoPolicyIsBadRequest(t *testing.T) {
	rv := &recVault{}
	ex := NewExecutor(rv, ExecutorConfig{AuthPath: "jwt-nomad", BoundAudience: "vault"})
	_, err := ex.UpdateGrants(context.Background(), InstanceRecord{Namespace: "acme"}, CredentialSpec{Source: SourceNone}, "plain-mcp",
		[]PathGrant{{Path: "secret/data/projects/x", Capabilities: []string{"read"}}})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("no policy: want ErrBadRequest, got %v", err)
	}
	if len(rv.ops) != 0 {
		t.Fatalf("must not touch Vault: %v", rv.ops)
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

// numeric normalizes int/float64 (a JSON round-trip may deliver either).
func numeric(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	default:
		panic(fmt.Sprintf("not numeric: %#v", v))
	}
}
