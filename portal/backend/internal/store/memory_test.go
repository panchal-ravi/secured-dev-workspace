package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

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
