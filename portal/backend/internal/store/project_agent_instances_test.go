package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func sampleInstance() ProjectAgentInstance {
	return ProjectAgentInstance{
		Project: "project-acme", Template: "support-triage", Subject: "alice@x",
		Namespace: "project-acme", Status: "running",
		JobID: "agent-project-acme-support-triage-abc123", Endpoint: "10.0.0.1:2500",
		LastActiveAt: time.Now(),
	}
}

func TestProjectAgentInstancesMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	saved, err := m.UpsertProjectAgentInstance(ctx, sampleInstance())
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	// re-upsert preserves creation time, updates status/job.
	in := sampleInstance()
	in.Status = "stopped"
	in.JobID = ""
	re, err := m.UpsertProjectAgentInstance(ctx, in)
	if err != nil || re.Status != "stopped" || !re.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("re-upsert must preserve created time: %+v err=%v", re, err)
	}

	// Ownership isolation: bob can't see alice's instance.
	if _, err := m.GetProjectAgentInstance(ctx, "project-acme", "support-triage", "bob@x"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("cross-owner leak: want ErrNotFound, got %v", err)
	}
	if list, _ := m.ListProjectAgentInstancesForOwner(ctx, "project-acme", "alice@x"); len(list) != 1 {
		t.Fatalf("owner list: %+v", list)
	}
	if list, _ := m.ListProjectAgentInstancesForOwner(ctx, "project-acme", "bob@x"); len(list) != 0 {
		t.Fatalf("owner list leak: %+v", list)
	}

	// Count is per (project, template), across owners.
	other := sampleInstance()
	other.Subject = "bob@x"
	if _, err := m.UpsertProjectAgentInstance(ctx, other); err != nil {
		t.Fatalf("upsert other owner: %v", err)
	}
	if n, _ := m.CountProjectAgentInstances(ctx, "project-acme", "support-triage"); n != 2 {
		t.Fatalf("count: want 2, got %d", n)
	}

	// Running list reflects status (alice is stopped, bob is running).
	if run, _ := m.ListRunningProjectAgentInstances(ctx); len(run) != 1 || run[0].Subject != "bob@x" {
		t.Fatalf("running list: %+v", run)
	}

	if err := m.DeleteProjectAgentInstance(ctx, "project-acme", "support-triage", "alice@x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := m.DeleteProjectAgentInstance(ctx, "project-acme", "support-triage", "alice@x"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}
	if _, err := m.UpsertProjectAgentInstance(ctx, ProjectAgentInstance{Template: "x", Subject: "y"}); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("empty project: want ErrBadRequest, got %v", err)
	}
}

func TestPostgresProjectAgentInstances(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_agent_instances`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := p.UpsertProjectAgentInstance(ctx, sampleInstance()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectAgentInstance(ctx, "project-acme", "support-triage", "alice@x")
	if err != nil || got.Namespace != "project-acme" || got.Status != "running" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if n, _ := p.CountProjectAgentInstances(ctx, "project-acme", "support-triage"); n != 1 {
		t.Fatalf("count: want 1, got %d", n)
	}
	if run, _ := p.ListRunningProjectAgentInstances(ctx); len(run) != 1 {
		t.Fatalf("running list: %+v", run)
	}
	if list, _ := p.ListProjectAgentInstancesForOwner(ctx, "project-acme", "alice@x"); len(list) != 1 {
		t.Fatalf("owner list: %+v", list)
	}
	if err := p.DeleteProjectAgentInstance(ctx, "project-acme", "support-triage", "alice@x"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := p.DeleteProjectAgentInstance(ctx, "project-acme", "support-triage", "alice@x"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("delete absent: want ErrNotFound, got %v", err)
	}
}
