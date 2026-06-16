package blueprint

import "testing"

func validClassC() BlueprintManifest {
	return BlueprintManifest{
		ID: "vault-mcp", Version: 1, Class: ClassC, Description: "Vault MCP",
		PolicyTpl: "path \"{{.Mount}}/data/projects/{{.Namespace}}/*\" { capabilities = [\"read\"] }",
		WIFRole:   WIFRoleSpec{NameTpl: "mcp-vault-mcp", TokenTTL: "1h"},
	}
}

func TestManifestValidate_ClassC_OK(t *testing.T) {
	if err := validClassC().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestManifestValidate_RejectsBadClass(t *testing.T) {
	m := validClassC()
	m.Class = "Z"
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for invalid class")
	}
}

func TestManifestValidate_ClassA_RequiresEngineAndRole(t *testing.T) {
	m := validClassC()
	m.Class = ClassA // but no engines/role/secret param
	if err := m.Validate(); err == nil {
		t.Fatal("class A without a database engine + role must be rejected")
	}
}

func TestContentHash_StableAndOrderIndependent(t *testing.T) {
	m := validClassC()
	h1 := m.ContentHash()
	// reordering params must not change the hash
	m.Params = []ParamSpec{{Name: "a", Type: "string"}, {Name: "b", Type: "string"}}
	hA := m.ContentHash()
	m.Params = []ParamSpec{{Name: "b", Type: "string"}, {Name: "a", Type: "string"}}
	hB := m.ContentHash()
	if hA != hB {
		t.Fatalf("content hash must be order-independent: %s != %s", hA, hB)
	}
	if h1 == "" || len(h1) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %q", h1)
	}
}

func TestJobCredentialValidation(t *testing.T) {
	base := BlueprintManifest{
		ID: "x", Version: 1, Class: ClassC, PolicyTpl: "path \"x\" {}",
		WIFRole: WIFRoleSpec{NameTpl: "r", TokenTTL: "1h"},
	}
	// Class C may omit env templates (VAULT_TOKEN is auto-provided by Nomad).
	if err := base.Validate(); err != nil {
		t.Fatalf("class C without env templates should validate: %v", err)
	}
	// Class A/B must declare at least one credential env template.
	a := BlueprintManifest{
		ID: "a", Version: 1, Class: ClassA, PolicyTpl: "p",
		WIFRole: WIFRoleSpec{NameTpl: "r", TokenTTL: "1h"},
		Engines: []EngineSpec{{Type: "database", MountPathTpl: "database/{{.Namespace}}-pg"}},
		Role:    &RoleSpec{NameTpl: "ro", CreationStatements: []string{"x"}},
		Params:  []ParamSpec{{Name: "p", Type: "secret", Required: true}},
	}
	if err := a.Validate(); err == nil {
		t.Fatalf("class A without job_credential env templates should fail")
	}
	a.JobCredential = JobCredentialSpec{EnvTemplates: map[string]string{
		"DATABASE_URI": `{{ with secret "${cred_path}" }}postgresql://{{.Data.username}}:{{.Data.password}}@${db_host}/${db_name}{{ end }}`,
	}}
	if err := a.Validate(); err != nil {
		t.Fatalf("class A with env templates should validate: %v", err)
	}
}

func TestRef_PinsIdVersionHash(t *testing.T) {
	m := validClassC()
	ref := m.Ref()
	if ref.ID != "vault-mcp" || ref.Version != 1 || ref.ContentHash != m.ContentHash() {
		t.Fatalf("ref must pin id+version+hash, got %+v", ref)
	}
}
