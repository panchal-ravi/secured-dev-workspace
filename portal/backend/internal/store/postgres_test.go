package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// newTestPostgres connects to the database in PORTAL_TEST_DB_DSN and starts each
// test from empty tables. Without that env var the Postgres tests are skipped, so
// the offline suite stays green; CI / a local `docker run postgres` sets it to
// exercise the real driver.
func newTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	dsn := os.Getenv("PORTAL_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("set PORTAL_TEST_DB_DSN to run the Postgres store tests")
	}
	p, err := NewPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	if _, err := p.db.Exec(`TRUNCATE mcp_servers, llm_models, audit_events RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return p
}

func TestPostgresLLMModel(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()

	if _, err := p.UpsertLLMModel(ctx, LLMModel{Name: "", Provider: "deepseek"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty name: want ErrBadRequest, got %v", err)
	}
	if _, err := p.UpsertLLMModel(ctx, LLMModel{Name: "deepseek-v4-pro", Provider: "deepseek", BackendModel: "deepseek/deepseek-chat", Status: StatusDraft}); err != nil {
		t.Fatalf("Upsert model: %v", err)
	}
	got, err := p.GetLLMModel(ctx, "deepseek-v4-pro")
	if err != nil || got.Provider != "deepseek" || got.BackendModel != "deepseek/deepseek-chat" {
		t.Fatalf("Get model: %+v err=%v", got, err)
	}
	if err := p.DeleteLLMModel(ctx, "deepseek-v4-pro"); err != nil {
		t.Fatalf("Delete model: %v", err)
	}
	if _, err := p.GetLLMModel(ctx, "deepseek-v4-pro"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("Get deleted: want ErrNotFound, got %v", err)
	}
}

func TestPostgresAudit(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()

	for _, a := range []string{"deploy", "test", "publish"} {
		if err := p.AppendAudit(ctx, AuditEvent{Actor: "admin@x", Action: a, Target: "vault-mcp", Outcome: "ok", Detail: map[string]any{"step": a}}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	events, _ := p.ListAudit(ctx, 2)
	if len(events) != 2 || events[0].Action != "publish" { // newest first
		t.Fatalf("ListAudit: %+v", events)
	}
	if events[0].ID == 0 || events[0].At.IsZero() {
		t.Fatalf("audit id/time not set: %+v", events[0])
	}
	if events[0].Detail["step"] != "publish" {
		t.Fatalf("audit detail JSONB not preserved: %+v", events[0].Detail)
	}

	all, _ := p.ListAudit(ctx, 0) // 0 = no limit
	if len(all) != 3 {
		t.Fatalf("ListAudit unlimited: len = %d, want 3", len(all))
	}
}
