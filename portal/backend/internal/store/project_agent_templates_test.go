package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func sampleTemplate() ProjectAgentTemplate {
	return ProjectAgentTemplate{
		Project: "project-acme", Name: "support-triage", Status: "draft", Version: 1,
		YAMLSource: "spec_version: v1\nname: support-triage\n", Model: "deepseek-v4-pro",
		ToolSelection: map[string][]string{"github": {"search"}},
		Wiring:        []TemplateWiring{{Server: "github", VirtualServerID: "vs-1", TokenName: "tok-a", KVPath: "secret/data/projects/agents/tmpl-support-triage/mcp/github"}},
		JobID:         "agent-project-acme-tmpl-support-triage", LLMKeyAlias: "llm-project-acme-tmpl-support-triage",
		CreatedBy: "adm@x",
	}
}

func TestProjectAgentTemplatesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	saved, err := m.UpsertProjectAgentTemplate(ctx, sampleTemplate())
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	// re-upsert preserves creation identity, advances version + status.
	in := sampleTemplate()
	in.Version = 2
	in.Status = "tested"
	in.CreatedBy = "someone-else@x"
	re, err := m.UpsertProjectAgentTemplate(ctx, in)
	if err != nil || re.Version != 2 || re.Status != "tested" || re.CreatedBy != "adm@x" || !re.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("re-upsert must preserve created identity: %+v err=%v", re, err)
	}

	got, err := m.GetProjectAgentTemplate(ctx, "project-acme", "support-triage")
	if err != nil || got.ToolSelection["github"][0] != "search" || len(got.Wiring) != 1 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := m.ListProjectAgentTemplates(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := m.ListProjectAgentTemplates(ctx, "project-beta"); len(list) != 0 {
		t.Fatalf("cross-project leak: %+v", list)
	}
	if err := m.DeleteProjectAgentTemplate(ctx, "project-acme", "support-triage"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectAgentTemplate(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if err := m.DeleteProjectAgentTemplate(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}
	if _, err := m.UpsertProjectAgentTemplate(ctx, ProjectAgentTemplate{Name: "x"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty project: want ErrBadRequest, got %v", err)
	}
}

func TestPostgresProjectAgentTemplates(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_agent_templates`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := p.UpsertProjectAgentTemplate(ctx, sampleTemplate()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectAgentTemplate(ctx, "project-acme", "support-triage")
	if err != nil || got.Model != "deepseek-v4-pro" || len(got.Wiring) != 1 || got.ToolSelection["github"][0] != "search" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := p.ListProjectAgentTemplates(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := p.DeleteProjectAgentTemplate(ctx, "project-acme", "support-triage"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeleteProjectAgentTemplate(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}
}
