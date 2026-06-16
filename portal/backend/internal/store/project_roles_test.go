package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestProjectRolesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	// grant is idempotent and returns the stored row with GrantedAt set.
	pr, err := m.GrantProjectRole(ctx, ProjectRole{Project: "project-acme", Subject: "alice@x", Role: "project-admin", GrantedBy: "pa@x"})
	if err != nil || pr.GrantedAt.IsZero() {
		t.Fatalf("grant: %+v err=%v", pr, err)
	}

	has, err := m.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin")
	if err != nil || !has {
		t.Fatalf("has own: %v err=%v", has, err)
	}
	// scope isolation: wrong project / wrong subject / wrong role → false
	for _, c := range [][3]string{{"project-beta", "alice@x", "project-admin"}, {"project-acme", "bob@x", "project-admin"}, {"project-acme", "alice@x", "viewer"}} {
		if has, _ := m.HasProjectRole(ctx, c[0], c[1], c[2]); has {
			t.Fatalf("has %v: want false", c)
		}
	}

	if rs, _ := m.ListProjectRoles(ctx, "project-acme"); len(rs) != 1 || rs[0].Subject != "alice@x" {
		t.Fatalf("list: %+v", rs)
	}
	if rs, _ := m.ProjectRolesForSubject(ctx, "alice@x"); len(rs) != 1 || rs[0].Project != "project-acme" {
		t.Fatalf("forSubject: %+v", rs)
	}

	if err := m.RevokeProjectRole(ctx, "project-acme", "alice@x", "project-admin"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if has, _ := m.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin"); has {
		t.Fatalf("has after revoke: want false")
	}
	if err := m.RevokeProjectRole(ctx, "project-acme", "alice@x", "project-admin"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("revoke absent: want ErrNotFound, got %v", err)
	}
}
