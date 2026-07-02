package projecttemplate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// fakeProjects satisfies ProjectLookup: returns the descriptor for a known project,
// membership error otherwise.
type fakeProjects struct{ ns string }

func (f fakeProjects) GetProject(_ context.Context, name string, _ []string) (descriptor.Descriptor, error) {
	if name != "project-beta" {
		return descriptor.Descriptor{}, apperr.ErrNotFound
	}
	return descriptor.Descriptor{ProjectName: name, Namespace: f.ns}, nil
}

func seed(t *testing.T) (*store.Memory, *Service) {
	t.Helper()
	st := store.NewMemory()
	ctx := context.Background()
	// A published base with one project-static + one per-workspace placeholder.
	src := `job "${namespace}" { image="${image}" repo="${git_repo_url}" name="${job_name}" }`
	if _, err := st.UpsertBaseJobTemplate(ctx, store.BaseJobTemplate{
		Name: "dev-workspace", Status: store.StatusPublished, Version: 1,
		PublishedSource: src, DefaultNodePool: "", Label: "Standard",
		Image: "ghcr.io/x/base:1",
	}); err != nil {
		t.Fatal(err)
	}
	// A minimal descriptor row so syncDescriptorFlavors can round-trip.
	js, _ := json.Marshal(descriptor.Descriptor{ProjectName: "project-beta", Namespace: "project-beta"})
	if _, err := st.UpsertProjectDescriptor(ctx, store.ProjectDescriptor{Project: "project-beta", Status: store.StatusReady, Descriptor: js}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, fakeProjects{ns: "project-beta"}, st, nil, nil, Config{LLMGatewayPrivateEndpoint: "http://10.0.0.1:4000"})
	return st, svc
}

func TestCreate_BakesPass1AndSyncsFlavors(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()

	pt, err := svc.Create(ctx, "admin@x", []string{"beta-developers"}, "project-beta", CreateInput{
		Base: "dev-workspace", GitRepoURL: "https://github.com/x/y", NodePool: "gpu",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Pass-1: project-static tokens baked, per-workspace ${job_name} survives.
	if !strings.Contains(pt.RenderedSource, `job "project-beta"`) ||
		!strings.Contains(pt.RenderedSource, `image="ghcr.io/x/base:1"`) ||
		!strings.Contains(pt.RenderedSource, `${job_name}`) {
		t.Fatalf("pass-1 bake wrong: %s", pt.RenderedSource)
	}
	if strings.Contains(pt.RenderedSource, `${image}`) || strings.Contains(pt.RenderedSource, `${namespace}`) {
		t.Fatalf("project-static tokens not baked: %s", pt.RenderedSource)
	}
	// Descriptor Flavors[] re-synced with the new flavor + its node pool.
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	d, _ := descriptor.Parse(string(pd.Descriptor))
	if len(d.Flavors) != 1 || d.Flavors[0].Name != "dev-workspace" || d.Flavors[0].NodePool != "gpu" {
		t.Fatalf("flavors not synced: %+v", d.Flavors)
	}
}

func TestCreate_UnpublishedBaseRejected(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()
	_, _ = st.UpsertBaseJobTemplate(ctx, store.BaseJobTemplate{Name: "draft-only", Status: store.StatusDraft, DraftSource: "x"})
	_, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "draft-only", GitRepoURL: "r"})
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

func TestCreate_MembershipEnforced(t *testing.T) {
	_, svc := seed(t)
	_, err := svc.Create(context.Background(), "admin@x", nil, "other-project", CreateInput{Base: "dev-workspace", GitRepoURL: "r"})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (membership)", err)
	}
}

func TestDelete_ResyncsFlavors(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", GitRepoURL: "r"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "admin@x", nil, "project-beta", "dev-workspace"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	d, _ := descriptor.Parse(string(pd.Descriptor))
	if len(d.Flavors) != 0 {
		t.Fatalf("flavors not cleared: %+v", d.Flavors)
	}
}
