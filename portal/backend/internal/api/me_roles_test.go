package api

import (
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
