package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestProjectDescriptorsMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	desc := json.RawMessage(`{"project_name":"project-acme","developers_group_name":"project-acme-developers","flavors":[]}`)
	in := ProjectDescriptor{Project: "project-acme", Status: StatusReady, Descriptor: desc, CreatedBy: "acme-admin@x"}
	saved, err := m.UpsertProjectDescriptor(ctx, in)
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	got, err := m.GetProjectDescriptor(ctx, "project-acme")
	if err != nil || got.Status != StatusReady || string(got.Descriptor) != string(desc) {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	// Upsert preserves CreatedAt/CreatedBy, updates status + blob.
	upd := ProjectDescriptor{Project: "project-acme", Status: StatusError, Descriptor: json.RawMessage(`{"project_name":"project-acme"}`), CreatedBy: "someone-else@x"}
	saved2, err := m.UpsertProjectDescriptor(ctx, upd)
	if err != nil || saved2.Status != StatusError || saved2.CreatedBy != "acme-admin@x" || !saved2.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("upsert-update did not preserve creation identity: %+v err=%v", saved2, err)
	}

	if list, _ := m.ListProjectDescriptors(ctx); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := m.DeleteProjectDescriptor(ctx, "project-acme"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectDescriptor(ctx, "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if err := m.DeleteProjectDescriptor(ctx, "missing"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete missing: want ErrNotFound, got %v", err)
	}
}

func TestProjectDescriptorEmptyProjectRejected(t *testing.T) {
	m := NewMemory()
	if _, err := m.UpsertProjectDescriptor(context.Background(), ProjectDescriptor{Status: StatusReady}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("want ErrBadRequest for empty project, got %v", err)
	}
}

func TestPostgresProjectDescriptors(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_descriptors`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	desc := json.RawMessage(`{"project_name":"project-acme","developers_group_name":"g","flavors":[]}`)
	if _, err := p.UpsertProjectDescriptor(ctx, ProjectDescriptor{Project: "project-acme", Status: StatusReady, Descriptor: desc, CreatedBy: "a@x"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectDescriptor(ctx, "project-acme")
	if err != nil || got.Status != StatusReady || string(got.Descriptor) != string(desc) {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	// Columns are authoritative on read (status reflects the update, not a stale blob).
	if _, err := p.UpsertProjectDescriptor(ctx, ProjectDescriptor{Project: "project-acme", Status: StatusError, Descriptor: desc}); err != nil {
		t.Fatalf("upsert-update: %v", err)
	}
	got2, _ := p.GetProjectDescriptor(ctx, "project-acme")
	if got2.Status != StatusError || got2.CreatedBy != "a@x" {
		t.Fatalf("update: status/created_by wrong: %+v", got2)
	}
	if list, _ := p.ListProjectDescriptors(ctx); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := p.DeleteProjectDescriptor(ctx, "project-acme"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := p.GetProjectDescriptor(ctx, "project-acme"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
}
