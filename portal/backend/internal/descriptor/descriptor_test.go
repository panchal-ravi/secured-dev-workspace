package descriptor

import "testing"

const raw = `{
  "project_name": "project-acme",
  "namespace": "project-acme",
  "project_scope_id": "p_abc",
  "credential_library_id": "clvlt_abc",
  "developers_group_name": "project-acme-developers",
  "boundary_oidc_auth_method_id": "amoidc_abc",
  "instance_private_ip": "10.0.0.5",
  "workspace_user": "dev",
  "alias_suffix": "boundary",
  "flavors": [
    {"name": "dev-workspace",
     "features": [{"key": "git-dynamic-pat", "label": "Git push", "description": "..."}]}
  ]
}`

func TestParse(t *testing.T) {
	d, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ProjectName != "project-acme" || d.CredentialLibraryID != "clvlt_abc" {
		t.Fatalf("unexpected descriptor: %+v", d)
	}
	f, ok := d.Flavor("dev-workspace")
	if !ok || len(f.Features) != 1 {
		t.Fatalf("flavor lookup failed: %+v ok=%v", f, ok)
	}
	if _, ok := d.Flavor("nope"); ok {
		t.Fatal("unknown flavor should not resolve")
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse(`{}`); err == nil {
		t.Fatal("expected error for descriptor without project_name")
	}
}

func TestAllowsGroupsCaseInsensitive(t *testing.T) {
	d := Descriptor{DevelopersGroupName: "Project-ACME-Developers"}
	if !d.AllowsGroups([]string{"other", "project-acme-developers"}) {
		t.Error("should match case-insensitively")
	}
	if d.AllowsGroups([]string{"readonly", "admins"}) {
		t.Error("should not match unrelated groups")
	}
	if d.AllowsGroups(nil) {
		t.Error("no groups should not match")
	}
}

func TestVisibleFilters(t *testing.T) {
	a := Descriptor{ProjectName: "a", DevelopersGroupName: "a-devs"}
	b := Descriptor{ProjectName: "b", DevelopersGroupName: "b-devs"}
	got := Visible([]Descriptor{a, b}, []string{"a-devs"})
	if len(got) != 1 || got[0].ProjectName != "a" {
		t.Fatalf("want only project a, got %+v", got)
	}
}
