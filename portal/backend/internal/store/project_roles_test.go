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

func TestPostgresProjectRoles(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_roles`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	if _, err := p.GrantProjectRole(ctx, ProjectRole{Project: "project-acme", Subject: "alice@x", Role: "project-admin", GrantedBy: "pa@x"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// re-grant is idempotent (upsert), not a duplicate-key error
	if _, err := p.GrantProjectRole(ctx, ProjectRole{Project: "project-acme", Subject: "alice@x", Role: "project-admin", GrantedBy: "pa2@x"}); err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if has, _ := p.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin"); !has {
		t.Fatalf("has: want true")
	}
	if rs, _ := p.ListProjectRoles(ctx, "project-acme"); len(rs) != 1 {
		t.Fatalf("list: %+v", rs)
	}
	if rs, _ := p.ProjectRolesForSubject(ctx, "alice@x"); len(rs) != 1 {
		t.Fatalf("forSubject: %+v", rs)
	}
	if err := p.RevokeProjectRole(ctx, "project-acme", "alice@x", "project-admin"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := p.RevokeProjectRole(ctx, "project-acme", "alice@x", "project-admin"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("revoke absent: want ErrNotFound, got %v", err)
	}
}

// TestPostgresLegacyRoleMigration proves the schema script rewrites legacy
// project-developer grants to project-user on (re-)application.
func TestPostgresLegacyRoleMigration(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_roles`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	if _, err := p.GrantProjectRole(ctx, ProjectRole{Project: "project-acme", Subject: "dev@x", Role: "project-developer", GrantedBy: "pa@x"}); err != nil {
		t.Fatalf("grant legacy: %v", err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if has, _ := p.HasProjectRole(ctx, "project-acme", "dev@x", "project-user"); !has {
		t.Fatalf("legacy grant not rewritten to project-user")
	}
	if has, _ := p.HasProjectRole(ctx, "project-acme", "dev@x", "project-developer"); has {
		t.Fatalf("legacy project-developer row still present")
	}
}
