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
	g := NewGuard(rs, nil, member)
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

type fakeCapStore struct {
	roles  []string            // DB grants returned for any subject
	matrix map[string][]string // stored matrix; nil = no row (defaults apply)
	err    error
}

func (f fakeCapStore) RolesForSubjectInProject(context.Context, string, string) ([]string, error) {
	return f.roles, f.err
}

func (f fakeCapStore) GetCapabilityMatrix(context.Context, string) (map[string][]string, bool, error) {
	return f.matrix, f.matrix != nil, f.err
}

// serveCap mirrors serve for RequireCapability.
func serveCap(t *testing.T, member MemberCheck, cs CapabilityStore, cap Capability) int {
	t.Helper()
	g := NewGuard(fakeRoleStore{}, cs, member)
	h := g.RequireCapability(cap)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mux := http.NewServeMux()
	mux.Handle("GET /api/projects/{name}/x", h)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/project-acme/x", nil)
	req = req.WithContext(auth.WithUser(context.Background(), auth.User{Email: "alice@x", Groups: []string{"project-acme-developers"}}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code
}

func TestRequireCapability(t *testing.T) {
	okMember := func(context.Context, string, []string) error { return nil }
	notMember := func(context.Context, string, []string) error { return fmt.Errorf("nope: %w", apperr.ErrForbidden) }

	cases := []struct {
		name   string
		member MemberCheck
		caps   CapabilityStore
		cap    Capability
		want   int
	}{
		// Plain member, no grants: implicit project-user → both capabilities.
		{"member-default-workspaces", okMember, fakeCapStore{}, CapWorkspaces, http.StatusOK},
		{"member-default-agents", okMember, fakeCapStore{}, CapAIAgents, http.StatusOK},
		// Stored matrix overriding defaults: project-users keep only workspaces...
		{"stored-user-workspaces-only", okMember, fakeCapStore{matrix: map[string][]string{
			string(RoleProjectUser): {string(CapWorkspaces)},
		}}, CapAIAgents, http.StatusForbidden},
		// ...or lose workspaces entirely.
		{"stored-user-no-workspaces", okMember, fakeCapStore{matrix: map[string][]string{
			string(RoleProjectUser): {},
		}}, CapWorkspaces, http.StatusForbidden},
		// An explicit project-admin grant is unaffected by a stripped project-user row.
		{"admin-grant-bypasses-stripped-user-row", okMember, fakeCapStore{
			roles: []string{string(RoleProjectAdmin)},
			matrix: map[string][]string{
				string(RoleProjectAdmin): {string(CapWorkspaces), string(CapAIAgents)},
				string(RoleProjectUser):  {},
			}}, CapWorkspaces, http.StatusOK},
		{"not-member", notMember, fakeCapStore{}, CapWorkspaces, http.StatusForbidden},
		{"store-error", okMember, fakeCapStore{err: fmt.Errorf("db down")}, CapWorkspaces, http.StatusBadGateway},
		{"nil-cap-store", okMember, nil, CapWorkspaces, http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := serveCap(t, c.member, c.caps, c.cap); got != c.want {
				t.Fatalf("status=%d want %d", got, c.want)
			}
		})
	}
}

func TestEffectiveCapabilities(t *testing.T) {
	m := DefaultCapabilityMatrix()
	got := EffectiveCapabilities(m, []string{string(RoleProjectUser)})
	want := []string{string(CapAIAgents), string(CapWorkspaces)}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("caps=%v want %v", got, want)
	}
	if caps := EffectiveCapabilities(m, []string{"unknown-role"}); len(caps) != 0 {
		t.Fatalf("unknown role got caps %v", caps)
	}
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
