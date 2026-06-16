package rbac

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
)

type fakeRoleStore struct {
	has bool
	err error
}

func (f fakeRoleStore) HasProjectRole(context.Context, string, string, string) (bool, error) {
	return f.has, f.err
}

// serve runs a RequireProjectRole-wrapped 200 handler for project "project-acme"
// with the given member-check result and role-store, returning the status code.
func serve(t *testing.T, member MemberCheck, rs ProjectRoleStore, email string) int {
	t.Helper()
	g := NewGuard(rs, member)
	h := g.RequireProjectRole(RoleProjectAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /api/projects/{name}/x", h)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/project-acme/x", nil)
	req = req.WithContext(auth.WithUser(context.Background(), auth.User{Email: email, Groups: []string{"project-acme-developers"}}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestRequireProjectRole(t *testing.T) {
	okMember := func(context.Context, string, []string) error { return nil }
	notMember := func(context.Context, string, []string) error { return fmt.Errorf("nope: %w", apperr.ErrForbidden) }
	infraErr := func(context.Context, string, []string) error { return fmt.Errorf("vault down") }

	cases := []struct {
		name   string
		member MemberCheck
		store  ProjectRoleStore
		want   int
	}{
		{"member+grant", okMember, fakeRoleStore{has: true}, http.StatusOK},
		{"member,no-grant", okMember, fakeRoleStore{has: false}, http.StatusForbidden},
		{"grant,not-member", notMember, fakeRoleStore{has: true}, http.StatusForbidden},
		{"infra-error", infraErr, fakeRoleStore{has: true}, http.StatusBadGateway},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serve(t, c.member, c.store, "alice@x"); got != c.want {
				t.Fatalf("status=%d want %d", got, c.want)
			}
		})
	}
}
