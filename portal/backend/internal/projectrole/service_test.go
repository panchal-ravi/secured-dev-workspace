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
	if err := svc.Revoke(ctx, "pa@x", "project-acme", "alice@x"); !errors.Is(err, apperr.ErrConflict) {
		t.Fatalf("revoke last admin: want ErrConflict, got %v", err)
	}

	// add a second admin, then the first can be revoked.
	if _, err := svc.Grant(ctx, "pa@x", "project-acme", "bob@x", "project-admin"); err != nil {
		t.Fatalf("grant bob: %v", err)
	}
	if err := svc.Revoke(ctx, "alice@x", "project-acme", "alice@x"); err != nil {
		t.Fatalf("revoke alice: %v", err)
	}
	if has, _ := st.HasProjectRole(ctx, "project-acme", "alice@x", "project-admin"); has {
		t.Fatalf("alice still admin after revoke")
	}
}
