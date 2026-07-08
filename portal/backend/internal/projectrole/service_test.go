package projectrole

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestGrantRevoke(t *testing.T) {
	st := store.NewMemory()
	svc := New(st)
	ctx := context.Background()

	// grant normalizes the subject (lowercased) and audits.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "Alice@X", "project-admin"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if has, _ := st.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin"); !has {
		t.Fatalf("grant did not normalize subject to lowercase")
	}
	if ev, _ := st.ListAudit(ctx, 10); len(ev) != 1 || ev[0].Action != "project-role.grant" {
		t.Fatalf("audit: %+v", ev)
	}

	// unknown role is rejected.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "bob@x", "wizard"); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("bad role: want ErrBadRequest, got %v", err)
	}

	// last-admin guard: revoking the only admin is a conflict.
	if err := svc.Revoke(ctx, "pa@x", "project-acme", "alice@x", "project-admin"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("revoke last admin: want ErrConflict, got %v", err)
	}

	// add a second admin, then the first can be revoked.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "bob@x", "project-admin"); err != nil {
		t.Fatalf("grant bob: %v", err)
	}
	if err := svc.Revoke(ctx, "alice@x", "project-acme", "alice@x", "project-admin"); err != nil {
		t.Fatalf("revoke alice: %v", err)
	}
	if has, _ := st.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin"); has {
		t.Fatalf("alice still admin after revoke")
	}
}

func TestCapabilities(t *testing.T) {
	st := store.NewMemory()
	svc := New(st)
	ctx := context.Background()

	// No stored row → built-in defaults (project-user has both), flagged as default.
	matrix, isDefault, err := svc.GetCapabilities(ctx, "project-acme")
	if err != nil || !isDefault || len(matrix["project-user"]) != 2 {
		t.Fatalf("defaults: matrix=%v isDefault=%v err=%v", matrix, isDefault, err)
	}

	full := map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
		"project-user":  {"workspaces"},
	}
	if err := svc.SetCapabilities(ctx, "pa@x", "project-acme", full); err != nil {
		t.Fatalf("set: %v", err)
	}
	matrix, isDefault, err = svc.GetCapabilities(ctx, "project-acme")
	if err != nil || isDefault || len(matrix["project-user"]) != 1 {
		t.Fatalf("stored: matrix=%v isDefault=%v err=%v", matrix, isDefault, err)
	}
	if ev, _ := st.ListAudit(ctx, 10); len(ev) != 1 || ev[0].Action != "project-capabilities.set" {
		t.Fatalf("audit: %+v", ev)
	}

	// Validation failures.
	bad := func(name string, m map[string][]string) {
		t.Helper()
		if err := svc.SetCapabilities(ctx, "pa@x", "project-acme", m); !errors.Is(err, apperr.ErrBadRequest) {
			t.Fatalf("%s: want ErrBadRequest, got %v", name, err)
		}
	}
	bad("unknown role", map[string][]string{
		"project-admin": {"workspaces", "ai-agents"}, "project-user": {}, "wizard": {},
	})
	bad("unknown capability", map[string][]string{
		"project-admin": {"workspaces", "ai-agents"}, "project-user": {"teleport"},
	})
	bad("missing role key", map[string][]string{
		"project-admin": {"workspaces", "ai-agents"},
	})
	// Lockout guard: project-admin must keep both capabilities.
	bad("admin lockout", map[string][]string{
		"project-admin": {"workspaces"}, "project-user": {"workspaces", "ai-agents"},
	})
}

// TestProjectUserRole proves project-user is a first-class grantable role,
// gated by the same allowed-set, and that revoking a project-user never trips
// the last-admin guard (which protects only the final project-admin).
func TestProjectUserRole(t *testing.T) {
	st := store.NewMemory()
	svc := New(st)
	ctx := context.Background()

	// project-user is grantable and normalizes + audits like admin.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "Dev@X", "project-user"); err != nil {
		t.Fatalf("grant project-user: %v", err)
	}
	if has, _ := st.HasProjectRole(ctx, "project-acme", "dev@x", "project-user"); !has {
		t.Fatalf("project-user grant not recorded")
	}

	// exactly one admin exists; revoking the sole *project-user* must NOT be
	// blocked by the last-admin guard.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "alice@x", "project-admin"); err != nil {
		t.Fatalf("grant admin: %v", err)
	}
	if err := svc.Revoke(ctx, "pa@x", "project-acme", "dev@x", "project-user"); err != nil {
		t.Fatalf("revoke project-user should not hit last-admin guard: %v", err)
	}
	if has, _ := st.HasProjectRole(ctx, "project-acme", "dev@x", "project-user"); has {
		t.Fatalf("project-user still present after revoke")
	}

	// the sole remaining admin is still protected.
	if err := svc.Revoke(ctx, "pa@x", "project-acme", "alice@x", "project-admin"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("revoke last admin: want ErrConflict, got %v", err)
	}
}
