package blueprint

import "testing"

func validClassC() BlueprintManifest {
	return BlueprintManifest{
		ID: "vault-mcp", Version: 1, Class: ClassC, Description: "Vault MCP",
		PolicyTpl: "path \"{{.Mount}}\" { capabilities = [\"read\"] }",
		WIFRole:   WIFRoleSpec{NameTpl: "mcp-vault-mcp", TokenPolicies: []string{"mcp-vault-mcp"}, TokenTTL: "1h"},
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

func TestRef_PinsIdVersionHash(t *testing.T) {
	m := validClassC()
	ref := m.Ref()
	if ref.ID != "vault-mcp" || ref.Version != 1 || ref.ContentHash != m.ContentHash() {
		t.Fatalf("ref must pin id+version+hash, got %+v", ref)
	}
}
