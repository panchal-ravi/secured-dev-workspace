package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestBaseJobTemplatesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	in := BaseJobTemplate{
		Name: "dev-workspace", Label: "Standard", Status: StatusPublished, Version: 1,
		ContentHash: "h1", DraftSource: `job "${job_name}" {}`, PublishedSource: `job "${job_name}" {}`,
		Features: []Feature{{Key: "git", Label: "Git", Description: "d"}}, CreatedBy: "admin@x",
	}
	saved, err := m.UpsertBaseJobTemplate(ctx, in)
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}
	// Version bump preserves creation identity.
	saved.Version, saved.Status, saved.CreatedBy = 2, StatusPublished, "other@x"
	up, err := m.UpsertBaseJobTemplate(ctx, saved)
	if err != nil || up.Version != 2 || up.CreatedBy != "admin@x" || !up.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("version bump did not preserve creation identity: %+v err=%v", up, err)
	}
	got, err := m.GetBaseJobTemplate(ctx, "dev-workspace")
	if err != nil || got.Version != 2 || len(got.Features) != 1 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := m.ListBaseJobTemplates(ctx); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := m.DeleteBaseJobTemplate(ctx, "dev-workspace"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetBaseJobTemplate(ctx, "dev-workspace"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if _, err := m.UpsertBaseJobTemplate(ctx, BaseJobTemplate{}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty name: want ErrBadRequest, got %v", err)
	}
}

func TestProjectTemplatesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	in := ProjectTemplate{
		Project: "project-acme", Flavor: "dev-workspace", Status: StatusReady, BaseVersion: 1,
		RenderedSource: `job "${job_name}" { namespace = "project-acme" }`, Image: "img:1",
		GitRepoURL: "https://x/y", CreatedBy: "acme-admin@x",
	}
	if _, err := m.UpsertProjectTemplate(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := m.GetProjectTemplate(ctx, "project-acme", "dev-workspace")
	if err != nil || got.Image != "img:1" || got.BaseVersion != 1 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := m.ListProjectTemplates(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := m.ListProjectTemplates(ctx, "project-beta"); len(list) != 0 {
		t.Fatalf("cross-project leak: %+v", list)
	}
	if err := m.DeleteProjectTemplate(ctx, "project-acme", "dev-workspace"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectTemplate(ctx, "project-acme", "dev-workspace"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if _, err := m.UpsertProjectTemplate(ctx, ProjectTemplate{Project: "p"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("missing flavor: want ErrBadRequest, got %v", err)
	}
}

func TestPostgresJobTemplates(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE base_job_templates, project_templates`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := p.UpsertBaseJobTemplate(ctx, BaseJobTemplate{Name: "dev-workspace", Status: StatusPublished, Version: 1, ContentHash: "h", PublishedSource: "src", CreatedBy: "a@x"}); err != nil {
		t.Fatalf("upsert base: %v", err)
	}
	// Columns authoritative on read after a version bump.
	if _, err := p.UpsertBaseJobTemplate(ctx, BaseJobTemplate{Name: "dev-workspace", Status: StatusPublished, Version: 2, ContentHash: "h2", PublishedSource: "src2"}); err != nil {
		t.Fatalf("bump: %v", err)
	}
	gb, _ := p.GetBaseJobTemplate(ctx, "dev-workspace")
	if gb.Version != 2 || gb.ContentHash != "h2" || gb.CreatedBy != "a@x" {
		t.Fatalf("base get: %+v", gb)
	}

	if _, err := p.UpsertProjectTemplate(ctx, ProjectTemplate{Project: "project-acme", Flavor: "dev-workspace", Status: StatusReady, BaseVersion: 2, RenderedSource: "rendered", Image: "img"}); err != nil {
		t.Fatalf("upsert project template: %v", err)
	}
	gp, err := p.GetProjectTemplate(ctx, "project-acme", "dev-workspace")
	if err != nil || gp.RenderedSource != "rendered" || gp.BaseVersion != 2 {
		t.Fatalf("project template get: %+v err=%v", gp, err)
	}
	if list, _ := p.ListProjectTemplates(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := p.DeleteProjectTemplate(ctx, "project-acme", "dev-workspace"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeleteBaseJobTemplate(ctx, "dev-workspace"); err != nil {
		t.Fatalf("delete base: %v", err)
	}
}
