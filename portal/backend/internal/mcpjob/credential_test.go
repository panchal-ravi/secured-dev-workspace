package mcpjob

import (
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
)

func TestCredentialEnv(t *testing.T) {
	jc := blueprint.JobCredentialSpec{EnvTemplates: map[string]string{
		"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}`,
	}}
	rec := blueprint.InstanceRecord{
		Namespace:     "project-acme",
		Mounts:        []string{"database/project-acme-pg"},
		WIFRoleName:   "mcp-postgres-mcp",
		LeasePrefixes: []string{"database/project-acme-pg/creds/mcp-ro"},
	}
	params := map[string]string{"db_host": "demo-db", "db_port": "5432", "db_name": "app"}

	env, err := CredentialEnv(jc, rec, params)
	if err != nil {
		t.Fatalf("CredentialEnv: %v", err)
	}
	got := env["DATABASE_URI"]
	want := `{{ with secret "database/project-acme-pg/creds/mcp-ro" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@demo-db:5432/app{{ end }}`
	if got != want {
		t.Fatalf("rendered env mismatch:\n got: %s\nwant: %s", got, want)
	}

	// a template referencing an unknown ${token} is a hard error (authoring gap).
	bad := blueprint.JobCredentialSpec{EnvTemplates: map[string]string{"X": "${nope}"}}
	if _, err := CredentialEnv(bad, rec, params); err == nil {
		t.Fatalf("unknown token should error")
	}
}
