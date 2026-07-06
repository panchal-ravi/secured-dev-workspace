package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func TestProjectMCPServersMemory(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()

	rec := json.RawMessage(`{"wif_role_name":"mcp-postgres-mcp","namespace":"project-acme"}`)
	in := ProjectMCPServer{
		Project: "project-acme", Name: "postgres-mcp", Status: StatusDeployed,
		BlueprintRef: &BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: "abc"},
		Instance:     rec, JobID: "j1", CreatedBy: "acme-admin@x",
	}
	saved, err := m.UpsertProjectMCPServer(ctx, in)
	if err != nil || saved.CreatedAt.IsZero() {
		t.Fatalf("upsert: %+v err=%v", saved, err)
	}

	got, err := m.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	if err != nil || got.JobID != "j1" || got.BlueprintRef.ID != "postgres-mcp" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := m.ListProjectMCPServers(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if list, _ := m.ListProjectMCPServers(ctx, "project-beta"); len(list) != 0 {
		t.Fatalf("cross-project leak: %+v", list)
	}
	if err := m.DeleteProjectMCPServer(ctx, "project-acme", "postgres-mcp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestPostgresProjectMCPServers(t *testing.T) {
	p := newTestPostgres(t)
	ctx := context.Background()
	if _, err := p.db.Exec(`TRUNCATE project_mcp_servers`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	in := ProjectMCPServer{Project: "project-acme", Name: "postgres-mcp", Status: StatusDeployed,
		BlueprintRef: &BlueprintRef{ID: "postgres-mcp", Version: 1, ContentHash: "abc"}, JobID: "j1"}
	if _, err := p.UpsertProjectMCPServer(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := p.GetProjectMCPServer(ctx, "project-acme", "postgres-mcp")
	if err != nil || got.BlueprintRef.ContentHash != "abc" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if list, _ := p.ListProjectMCPServers(ctx, "project-acme"); len(list) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if err := p.DeleteProjectMCPServer(ctx, "project-acme", "postgres-mcp"); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// A row persisted before Phase F carried blueprint_ref as a VALUE (not a
// pointer) plus the opaque instance blob. Both must keep decoding: the instance
// drives teardown, and a populated legacy ref must land on the pointer field.
func TestProjectMCPServer_LegacyRowJSONDecodes(t *testing.T) {
	legacy := []byte(`{
		"project": "project-acme",
		"name": "postgres-mcp",
		"status": "deployed",
		"blueprint_ref": {"id": "postgres-mcp", "version": 1, "content_hash": "abc123"},
		"instance": {"ref":{"id":"postgres-mcp","version":1,"content_hash":"abc123"},
			"namespace":"project-acme",
			"mounts":["database/project-acme-pg"],
			"policy_names":["mcp-postgres-mcp"],
			"wif_role_name":"mcp-postgres-mcp",
			"lease_prefixes":["database/project-acme-pg/creds/mcp-ro"]},
		"transport": "sse",
		"created_by": "admin@x"
	}`)
	var s ProjectMCPServer
	if err := json.Unmarshal(legacy, &s); err != nil {
		t.Fatalf("legacy row must decode: %v", err)
	}
	if s.BlueprintRef == nil || s.BlueprintRef.ID != "postgres-mcp" || s.BlueprintRef.ContentHash != "abc123" {
		t.Fatalf("legacy blueprint_ref: %+v", s.BlueprintRef)
	}
	var inst struct {
		LeasePrefixes []string `json:"lease_prefixes"`
	}
	if err := json.Unmarshal(s.Instance, &inst); err != nil || len(inst.LeasePrefixes) != 1 {
		t.Fatalf("instance blob must stay usable for teardown: %v %+v", err, inst)
	}
	// And a wizard-era row without a ref round-trips with the pointer nil.
	blob, _ := json.Marshal(ProjectMCPServer{Project: "p", Name: "n", Image: "img", Port: 8080})
	var s2 ProjectMCPServer
	if err := json.Unmarshal(blob, &s2); err != nil || s2.BlueprintRef != nil || s2.Image != "img" {
		t.Fatalf("wizard row round-trip: %v %+v", err, s2)
	}
}
