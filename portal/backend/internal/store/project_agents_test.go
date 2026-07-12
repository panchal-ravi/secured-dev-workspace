package store

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestProjectAgentsMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	in := ProjectAgent{
		Project: "project-acme", Name: "support-triage", Status: StatusDeployed, Version: 1,
		YAMLSource: "spec_version: v1\nname: support-triage\n", Model: "deepseek-v4-pro",
		MCPServers: []string{"github"}, JobID: "agent-project-acme-support-triage",
		LLMKeyAlias: "llm-project-acme-agent-support-triage", CreatedBy: "pow@x",
	}
	saved, err := m.UpsertProjectAgent(ctx, in)
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	// re-upsert preserves creation identity and bumps version wholesale.
	in.Version = 2
	in.CreatedBy = "someone-else@x"
	re, err := m.UpsertProjectAgent(ctx, in)
	if err != nil || re.Version != 2 || re.CreatedBy != "pow@x" || !re.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("re-upsert must preserve created identity: %+v err=%v", re, err)
	}

	got, err := m.GetProjectAgent(ctx, "project-acme", "support-triage")
	if err != nil || got.JobID != "agent-project-acme-support-triage" || got.LLMKeyAlias == "" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := m.ListProjectAgents(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := m.ListProjectAgents(ctx, "project-beta"); len(list) != 0 {
		t.Fatalf("cross-project leak: %+v", list)
	}
	if err := m.DeleteProjectAgent(ctx, "project-acme", "support-triage"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectAgent(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	if err := m.DeleteProjectAgent(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}

	if _, err := m.UpsertProjectAgent(ctx, ProjectAgent{Name: "x"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty project: want ErrBadRequest, got %v", err)
	}
}

func TestPostgresProjectAgents(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_agents`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	in := ProjectAgent{
		Project: "project-acme", Name: "support-triage", Status: StatusDeployed, Version: 1,
		YAMLSource: "spec_version: v1\n", Model: "deepseek-v4-pro", MCPServers: []string{"github"},
		JobID: "j1", LLMKeyAlias: "llm-project-acme-agent-support-triage",
	}
	if _, err := p.UpsertProjectAgent(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectAgent(ctx, "project-acme", "support-triage")
	if err != nil || got.Model != "deepseek-v4-pro" || len(got.MCPServers) != 1 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := p.ListProjectAgents(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := p.DeleteProjectAgent(ctx, "project-acme", "support-triage"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeleteProjectAgent(ctx, "project-acme", "support-triage"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}
}
