package api

import (
	"context"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestIntersectProjectRoles(t *testing.T) {
	grants := []store.ProjectRole{
		{Project: "project-acme", Role: "project-admin"},
		{Project: "project-stale", Role: "project-admin"}, // not a current member
	}
	members := map[string]bool{"project-acme": true}

	got := intersectProjectRoles(grants, members)
	if len(got) != 1 || got[0].Project != "project-acme" || got[0].Role != "project-admin" {
		t.Fatalf("intersect: %+v", got)
	}
}

// TestCapStoreAdapter proves the store.Store → rbac.CapabilityStore adapter:
// per-project role filtering and the no-row-means-defaults contract.
func TestCapStoreAdapter(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	cs := capStore{st: st}

	if _, err := st.GrantProjectRole(ctx, store.ProjectRole{Project: "project-acme", Subject: "alice@x", Role: "project-user"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := st.GrantProjectRole(ctx, store.ProjectRole{Project: "project-other", Subject: "alice@x", Role: "project-admin"}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	roles, err := cs.RolesForSubjectInProject(ctx, "project-acme", "alice@x")
	if err != nil || len(roles) != 1 || roles[0] != "project-user" {
		t.Fatalf("roles: %v err=%v", roles, err)
	}

	// No stored matrix → stored=false, nil matrix (caller applies defaults).
	if m, stored, err := cs.GetCapabilityMatrix(ctx, "project-acme"); err != nil || stored || m != nil {
		t.Fatalf("matrix absent: %v stored=%v err=%v", m, stored, err)
	}
	if _, err := st.UpsertProjectCapabilities(ctx, store.ProjectCapabilities{Project: "project-acme", Matrix: map[string][]string{"project-user": {"ai-agents"}}}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if m, stored, err := cs.GetCapabilityMatrix(ctx, "project-acme"); err != nil || !stored || len(m["project-user"]) != 1 {
		t.Fatalf("matrix stored: %v stored=%v err=%v", m, stored, err)
	}
}
