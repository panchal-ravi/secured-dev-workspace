package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestProjectCapabilitiesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	matrix := map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
		"project-user":  {"workspaces", "ai-agents"},
	}
	saved, err := m.UpsertProjectCapabilities(ctx, ProjectCapabilities{Project: "project-acme", Matrix: matrix, CreatedBy: "acme-admin@x"})
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	got, err := m.GetProjectCapabilities(ctx, "project-acme")
	if err != nil || len(got.Matrix["project-user"]) != 2 {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	// Upsert preserves creation identity, replaces the matrix wholesale.
	upd := map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
		"project-user":  {"workspaces"},
	}
	saved2, err := m.UpsertProjectCapabilities(ctx, ProjectCapabilities{Project: "project-acme", Matrix: upd, CreatedBy: "someone-else@x"})
	if err != nil || saved2.CreatedBy != "acme-admin@x" || !saved2.CreatedAt.Equal(saved.CreatedAt) || len(saved2.Matrix["project-user"]) != 1 {
		t.Fatalf("upsert-update: %+v err=%v", saved2, err)
	}

	if err := m.DeleteProjectCapabilities(ctx, "project-acme"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectCapabilities(ctx, "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	// Delete is idempotent — cleanup must not fail for projects without a row.
	if err := m.DeleteProjectCapabilities(ctx, "missing"); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if _, err := m.UpsertProjectCapabilities(ctx, ProjectCapabilities{Matrix: matrix}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("want ErrBadRequest for empty project, got %v", err)
	}
}

func TestPostgresProjectCapabilities(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_capabilities`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	matrix := map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
		"project-user":  {"workspaces", "ai-agents"},
	}
	if _, err := p.UpsertProjectCapabilities(ctx, ProjectCapabilities{Project: "project-acme", Matrix: matrix, CreatedBy: "a@x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectCapabilities(ctx, "project-acme")
	if err != nil || len(got.Matrix["project-user"]) != 2 || got.CreatedBy != "a@x" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	upd := map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
		"project-user":  {"workspaces"},
	}
	if _, err := p.UpsertProjectCapabilities(ctx, ProjectCapabilities{Project: "project-acme", Matrix: upd}); err != nil {
		t.Fatalf("upsert-update: %v", err)
	}
	got2, _ := p.GetProjectCapabilities(ctx, "project-acme")
	if len(got2.Matrix["project-user"]) != 1 || got2.CreatedBy != "a@x" {
		t.Fatalf("update: %+v", got2)
	}
	if err := p.DeleteProjectCapabilities(ctx, "project-acme"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := p.GetProjectCapabilities(ctx, "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if err := p.DeleteProjectCapabilities(ctx, "project-acme"); err != nil {
		t.Fatalf("delete idempotent: %v", err)
	}
}
