package projectbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// fakeNomad records the teardown calls DeleteProject makes.
type fakeNomad struct {
	ops  []string
	jobs []string // pre-seeded job IDs in the project namespace
	vols []string // pre-seeded dynamic host volume names in the namespace
}

func (f *fakeNomad) CreateNamespace(name, _ string) error {
	f.ops = append(f.ops, "create-ns "+name)
	return nil
}
func (f *fakeNomad) UpsertACLPolicy(name, _, _ string) error {
	f.ops = append(f.ops, "upsert-policy "+name)
	return nil
}
func (f *fakeNomad) CreateBindingRule(_, _, bind string) error {
	f.ops = append(f.ops, "create-rule "+bind)
	return nil
}
func (f *fakeNomad) ListJobIDs(ns string) ([]string, error)         { return f.jobs, nil }
func (f *fakeNomad) PurgeJob(ns, id string) error                   { f.ops = append(f.ops, "purge "+id); return nil }
func (f *fakeNomad) ListCSIVolumeNames(ns string) ([]string, error) { return f.vols, nil }
func (f *fakeNomad) DeleteHostVolume(ns, name string) error {
	f.ops = append(f.ops, "delete-vol "+name)
	return nil
}
func (f *fakeNomad) DeleteNamespace(name string) error {
	f.ops = append(f.ops, "delete-ns "+name)
	return nil
}
func (f *fakeNomad) DeleteACLPolicy(name string) error {
	f.ops = append(f.ops, "delete-policy "+name)
	return nil
}
func (f *fakeNomad) DeleteBindingRulesForPolicy(bind string) error {
	f.ops = append(f.ops, "delete-rule "+bind)
	return nil
}

// fakeBoundary records scope operations.
type fakeBoundary struct{ ops []string }

func (f *fakeBoundary) CreateProjectScope(_ context.Context, _, name, _ string) (string, error) {
	f.ops = append(f.ops, "create-scope "+name)
	return "p_test", nil
}
func (f *fakeBoundary) CreateHostCatalog(_ context.Context, scopeID, name string) (string, error) {
	f.ops = append(f.ops, "create-host-catalog "+scopeID+"/"+name)
	return "hcst_test", nil
}
func (f *fakeBoundary) DeleteScope(_ context.Context, id string) error {
	f.ops = append(f.ops, "delete-scope "+id)
	return nil
}

// seedDetail stores a ready descriptor row for the detail/edit/delete tests.
func seedDetail(t *testing.T) (*store.Memory, *fakeNomad, *fakeBoundary, *Service) {
	t.Helper()
	st := store.NewMemory()
	fn := &fakeNomad{}
	fb := &fakeBoundary{}
	js, _ := json.Marshal(descriptor.Descriptor{
		ProjectName: "project-acme", Namespace: "project-acme", ProjectScopeID: "p_acme",
		DevelopersGroupName: "project-acme-developers", WorkspaceUser: "dev",
	})
	if _, err := st.UpsertProjectDescriptor(context.Background(), store.ProjectDescriptor{
		Project: "project-acme", Status: store.StatusReady, Descriptor: js, CreatedBy: "admin@x",
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(nil, fn, fb, st, nil, nil, nil, nil, st, Config{})
	return st, fn, fb, svc
}

func TestGetProject_ReturnsDescriptorAndRowMeta(t *testing.T) {
	_, _, _, svc := seedDetail(t)
	d, err := svc.GetProject(context.Background(), "project-acme")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if d.DevelopersGroupName != "project-acme-developers" || d.Status != store.StatusReady || d.CreatedBy != "admin@x" {
		t.Fatalf("detail mismatch: %+v", d)
	}
	if _, err := svc.GetProject(context.Background(), "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown project: err = %v, want ErrNotFound", err)
	}
}

// UpdateProject edits only the developers group, preserving every other
// descriptor field and the row's creation identity.
func TestUpdateProject_EditsGroupOnly(t *testing.T) {
	st, _, _, svc := seedDetail(t)
	ctx := context.Background()

	if _, err := svc.UpdateProject(ctx, "admin@x", "project-acme", UpdateProjectInput{}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty group: err = %v, want ErrBadRequest", err)
	}

	d, err := svc.UpdateProject(ctx, "admin@x", "project-acme", UpdateProjectInput{DevelopersGroupName: "acme-team"})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if d.DevelopersGroupName != "acme-team" || d.WorkspaceUser != "dev" || d.Namespace != "project-acme" {
		t.Fatalf("update mismatch: %+v", d)
	}
	pd, _ := st.GetProjectDescriptor(ctx, "project-acme")
	dd, _ := descriptor.Parse(string(pd.Descriptor))
	if dd.DevelopersGroupName != "acme-team" || pd.CreatedBy != "admin@x" {
		t.Fatalf("persisted row mismatch: %+v / %+v", dd, pd)
	}
}

// DeleteProject sweeps every plane and removes the store rows; the descriptor
// goes last and only after all planes confirmed.
func TestDeleteProject_SweepsAllPlanes(t *testing.T) {
	st, fn, fb, svc := seedDetail(t)
	ctx := context.Background()
	fn.jobs = []string{"ws-dev-abc", "mcp-project-acme-vault-mcp"}
	fn.vols = []string{"ws-dev-abc-home"}

	// Seed the per-project rows the other planes created.
	if _, err := st.UpsertProjectTemplate(ctx, store.ProjectTemplate{Project: "project-acme", Flavor: "dev-workspace", Status: store.StatusReady}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GrantProjectRole(ctx, store.ProjectRole{Project: "project-acme", Subject: "a@x", Role: "project-admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertProjectMCPServer(ctx, store.ProjectMCPServer{Project: "project-acme", Name: "vault-mcp", Status: "deployed", JobID: "mcp-project-acme-vault-mcp", PeerID: "peer-1"}); err != nil {
		t.Fatal(err)
	}

	if err := svc.DeleteProject(ctx, "admin@x", "project-acme"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	for _, want := range []string{"purge mcp-project-acme-vault-mcp", "purge ws-dev-abc", "delete-vol ws-dev-abc-home", "delete-ns project-acme", "delete-rule project-project-acme-dev", "delete-policy project-project-acme-dev"} {
		if !slicesContains(fn.ops, want) {
			t.Fatalf("missing nomad op %q in %v", want, fn.ops)
		}
	}
	// The volume sweep must come BEFORE the namespace delete — a volume left
	// behind becomes an undeletable orphan once the namespace is gone.
	volIdx, nsIdx := -1, -1
	for i, op := range fn.ops {
		if op == "delete-vol ws-dev-abc-home" {
			volIdx = i
		}
		if op == "delete-ns project-acme" {
			nsIdx = i
		}
	}
	if volIdx > nsIdx {
		t.Fatalf("volume sweep must precede namespace delete: %v", fn.ops)
	}
	if !slicesContains(fb.ops, "delete-scope p_acme") {
		t.Fatalf("boundary scope not deleted: %v", fb.ops)
	}
	if _, err := st.GetProjectDescriptor(ctx, "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("descriptor should be gone, err = %v", err)
	}
	if rows, _ := st.ListProjectMCPServers(ctx, "project-acme"); len(rows) != 0 {
		t.Fatalf("mcp rows should be gone: %v", rows)
	}
	if tmpls, _ := st.ListProjectTemplates(ctx, "project-acme"); len(tmpls) != 0 {
		t.Fatalf("template rows should be gone: %v", tmpls)
	}
	if prs, _ := st.ListProjectRoles(ctx, "project-acme"); len(prs) != 0 {
		t.Fatalf("role rows should be gone: %v", prs)
	}

	if err := svc.DeleteProject(ctx, "admin@x", "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("second delete: err = %v, want ErrNotFound", err)
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
