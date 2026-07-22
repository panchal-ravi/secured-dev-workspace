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

// Update: metadata-only edits keep the baked source; a repo change re-bakes from
// the current published base preserving add-ons; running workspaces are untouched
// (launch reads the template row at launch time).
func TestUpdate_MetadataAndRepoRebake(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", GitRepoURL: "https://github.com/x/old"}); err != nil {
		t.Fatal(err)
	}

	// Metadata-only: label/node_pool change, source untouched.
	before, _ := st.GetProjectTemplate(ctx, "project-beta", "dev-workspace")
	pt, err := svc.Update(ctx, "admin@x", nil, "project-beta", "dev-workspace", UpdateInput{Label: "Renamed", NodePool: "gpu"})
	if err != nil {
		t.Fatalf("Update (metadata): %v", err)
	}
	if pt.Label != "Renamed" || pt.NodePool != "gpu" {
		t.Fatalf("metadata not updated: %+v", pt)
	}
	if pt.RenderedSource != before.RenderedSource {
		t.Fatalf("metadata-only update must not re-bake the source")
	}

	// Repo change: re-baked with the new URL, descriptor flavors resynced.
	pt, err = svc.Update(ctx, "admin@x", nil, "project-beta", "dev-workspace", UpdateInput{GitRepoURL: "https://github.com/x/new"})
	if err != nil {
		t.Fatalf("Update (repo): %v", err)
	}
	if !strings.Contains(pt.RenderedSource, `repo="https://github.com/x/new"`) {
		t.Fatalf("repo not re-baked into source: %s", pt.RenderedSource)
	}
	if pt.Label != "Renamed" {
		t.Fatalf("earlier metadata lost on re-bake: %+v", pt)
	}
	pd, _ := st.GetProjectDescriptor(ctx, "project-beta")
	d, _ := descriptor.Parse(string(pd.Descriptor))
	if len(d.Flavors) != 1 || d.Flavors[0].GitRepoURL != "https://github.com/x/new" || d.Flavors[0].NodePool != "gpu" {
		t.Fatalf("descriptor flavors not resynced: %+v", d.Flavors)
	}

	// Unknown flavor → not found.
	if _, err := svc.Update(ctx, "admin@x", nil, "project-beta", "nope", UpdateInput{Label: "x"}); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown flavor: err = %v, want ErrNotFound", err)
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

func TestCreate_CodingAgentSelectionAndAllowList(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()

	// Default (no agent given) → claude, and the claude feature card is attached.
	pt, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", GitRepoURL: "r"})
	if err != nil {
		t.Fatalf("Create default: %v", err)
	}
	if pt.CodingAgent != "claude" {
		t.Fatalf("default agent = %q, want claude", pt.CodingAgent)
	}
	if !hasFeature(pt.Features, "claude-deepseek") {
		t.Fatalf("claude flavor missing claude feature card: %+v", pt.Features)
	}

	// Explicit bob → recorded as bob with the bob feature card.
	pt, err = svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", Flavor: "std-bob", CodingAgent: "bob", GitRepoURL: "r"})
	if err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	if pt.CodingAgent != "bob" || !hasFeature(pt.Features, "bob-shell-ibm-hosted") {
		t.Fatalf("bob flavor wrong: agent=%q features=%+v", pt.CodingAgent, pt.Features)
	}

	// Unknown agent is rejected.
	if _, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", Flavor: "x", CodingAgent: "grok", GitRepoURL: "r"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("unknown agent: err = %v, want ErrBadRequest", err)
	}

	// A disabled agent is rejected by the allow-list.
	if err := st.UpsertCodingAgentSetting(ctx, store.CodingAgentSetting{Key: "bob", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, "admin@x", nil, "project-beta", CreateInput{Base: "dev-workspace", Flavor: "x", CodingAgent: "bob", GitRepoURL: "r"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("disabled agent: err = %v, want ErrBadRequest", err)
	}
}

func TestListCodingAgents_ReflectsAllowList(t *testing.T) {
	st, svc := seed(t)
	ctx := context.Background()

	agents, err := svc.ListCodingAgents(ctx, nil, "project-beta")
	if err != nil {
		t.Fatalf("ListCodingAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("want both agents enabled by default, got %d", len(agents))
	}

	if err := st.UpsertCodingAgentSetting(ctx, store.CodingAgentSetting{Key: "bob", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	agents, _ = svc.ListCodingAgents(ctx, nil, "project-beta")
	if len(agents) != 1 || agents[0].Key != "claude" {
		t.Fatalf("disabled bob should drop from picker, got %+v", agents)
	}
}

func hasFeature(fs []store.Feature, key string) bool {
	for _, f := range fs {
		if f.Key == key {
			return true
		}
	}
	return false
}
