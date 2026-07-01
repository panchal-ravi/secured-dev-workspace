package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestMemoryMCPServerCRUD(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	if _, err := m.GetMCPServer(ctx, "absent"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("Get absent: want ErrNotFound, got %v", err)
	}

	s, err := m.UpsertMCPServer(ctx, MCPServer{Name: "vault-mcp", Image: "hashicorp/vault-mcp-server", Status: StatusDraft, CreatedBy: "admin@x"})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not set: %+v", s)
	}

	// update preserves CreatedAt/CreatedBy
	s.Status = StatusPublished
	s.CreatedBy = "" // caller may not resend it
	updated, _ := m.UpsertMCPServer(ctx, s)
	if updated.Status != StatusPublished || updated.CreatedBy != "admin@x" || !updated.CreatedAt.Equal(s.CreatedAt) {
		t.Fatalf("update did not preserve create metadata: %+v", updated)
	}

	list, _ := m.ListMCPServers(ctx)
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if err := m.DeleteMCPServer(ctx, "vault-mcp"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := m.DeleteMCPServer(ctx, "vault-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
}

func TestMemoryAudit(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	for _, a := range []string{"deploy", "test", "publish"} {
		if err := m.AppendAudit(ctx, AuditEvent{Actor: "admin@x", Action: a, Target: "vault-mcp", Outcome: "ok"}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	events, _ := m.ListAudit(ctx, 2)
	if len(events) != 2 || events[0].Action != "publish" { // newest first
		t.Fatalf("ListAudit: %+v", events)
	}
	if events[0].ID == 0 || events[0].At.IsZero() {
		t.Fatalf("audit id/time not set: %+v", events[0])
	}
}

func TestMemoryLLMModel(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	if _, err := m.UpsertLLMModel(ctx, LLMModel{Name: "", Provider: "deepseek"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty name: want ErrBadRequest, got %v", err)
	}
	if _, err := m.UpsertLLMModel(ctx, LLMModel{Name: "deepseek-v4-pro", Provider: "deepseek", Status: StatusDraft}); err != nil {
		t.Fatalf("Upsert model: %v", err)
	}
	got, err := m.GetLLMModel(ctx, "deepseek-v4-pro")
	if err != nil || got.Provider != "deepseek" {
		t.Fatalf("Get model: %+v err=%v", got, err)
	}
}

func TestMemory_BlueprintRoundTrip(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	bp := Blueprint{ID: "vault-mcp", Version: 1, Class: "C", ContentHash: "abc", Status: StatusDraft, CreatedBy: "admin@x"}
	saved, err := m.UpsertBlueprint(ctx, bp)
	if err != nil {
		t.Fatal(err)
	}
	if saved.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be stamped")
	}
	got, err := m.GetBlueprint(ctx, "vault-mcp", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentHash != "abc" || got.Status != StatusDraft {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	list, err := m.ListBlueprints(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 blueprint, got %d (%v)", len(list), err)
	}
}

func TestMemory_Blueprint_ManifestRoundTrip(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	in := Blueprint{ID: "vault-mcp", Version: 1, Class: "C", ContentHash: "h", Status: StatusDraft,
		Manifest: json.RawMessage(`{"id":"vault-mcp","class":"C"}`)}
	if _, err := m.UpsertBlueprint(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := m.GetBlueprint(ctx, "vault-mcp", 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Manifest) != `{"id":"vault-mcp","class":"C"}` {
		t.Fatalf("manifest not round-tripped: %s", got.Manifest)
	}
}

func TestMemory_GetBlueprint_NotFound(t *testing.T) {
	m := NewMemory()
	if _, err := m.GetBlueprint(context.Background(), "nope", 9); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
