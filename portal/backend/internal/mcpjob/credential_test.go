package mcpjob

import (
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
)

func TestCredentialEnv(t *testing.T) {
	env_tpls := map[string]string{
		"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}`,
	}
	rec := blueprint.InstanceRecord{
		Namespace:     "project-acme",
		Mounts:        []string{"database/project-acme-pg"},
		WIFRoleName:   "mcp-postgres-mcp",
		LeasePrefixes: []string{"database/project-acme-pg/creds/mcp-ro"},
	}
	params := map[string]string{"db_host": "demo-db", "db_port": "5432", "db_name": "app"}

	env, err := CredentialEnv(env_tpls, rec, params)
	if err != nil {
		t.Fatalf("CredentialEnv: %v", err)
	}
	got := env["DATABASE_URI"]
	want := `{{ with secret "database/project-acme-pg/creds/mcp-ro" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@demo-db:5432/app{{ end }}`
	if got != want {
		t.Fatalf("rendered env mismatch:\n got: %s\nwant: %s", got, want)
	}

	// a template referencing an unknown ${token} is a hard error (authoring gap).
	bad := map[string]string{"X": "${nope}"}
	if _, err := CredentialEnv(bad, rec, params); err == nil {
		t.Fatalf("unknown token should error")
	}
}

// A static-source record has no lease prefixes — ${cred_path} must come from the explicit
// CredPath (the KV v2 DATA path), not render empty.
func TestCredentialEnv_ClassB_KVCredPath(t *testing.T) {
	env_tpls := map[string]string{
		"API_KEY": `{{ with secret "${cred_path}" }}{{ .Data.data.api_key }}{{ end }}`,
	}
	rec := blueprint.InstanceRecord{
		Namespace:   "project-acme",
		WIFRoleName: "mcp-generic-api-key",
		CredPath:    "secret/data/projects/generic-api-key",
	}
	env, err := CredentialEnv(env_tpls, rec, nil)
	if err != nil {
		t.Fatalf("CredentialEnv: %v", err)
	}
	want := `{{ with secret "secret/data/projects/generic-api-key" }}{{ .Data.data.api_key }}{{ end }}`
	if env["API_KEY"] != want {
		t.Fatalf("rendered env mismatch:\n got: %s\nwant: %s", env["API_KEY"], want)
	}
}

// A record persisted before CredPath existed (Class A only) must render exactly
// as before via the lease-prefix fallback.
func TestCredentialEnv_LegacyRecordFallsBackToLeasePrefix(t *testing.T) {
	env_tpls := map[string]string{
		"P": `${cred_path}`,
	}
	rec := blueprint.InstanceRecord{
		Namespace:     "project-acme",
		WIFRoleName:   "mcp-postgres-mcp",
		LeasePrefixes: []string{"database/project-acme-pg/creds/mcp-ro"},
	}
	env, err := CredentialEnv(env_tpls, rec, nil)
	if err != nil {
		t.Fatalf("CredentialEnv: %v", err)
	}
	if env["P"] != "database/project-acme-pg/creds/mcp-ro" {
		t.Fatalf("legacy fallback broken: %q", env["P"])
	}
}
