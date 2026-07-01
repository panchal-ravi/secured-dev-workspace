package rbac

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// RoleProjectAdmin can manage MCP servers and member roles within one project.
// It is a DB elevation of a project member, not a Verify group.
const RoleProjectAdmin Role = "project-admin"

// RoleProjectDeveloper marks a project member as a developer within one project.
// Like project-admin it is a Portal-DB elevation gated by the project's single
// Verify group — the admin/developer distinction lives here, not in the IdP.
const RoleProjectDeveloper Role = "project-developer"

// ProjectRoleStore is the read side of project-role grants the guard consults
// (satisfied by *store.Memory / *store.Postgres).
type ProjectRoleStore interface {
	HasProjectRole(ctx context.Context, project, subject, role string) (bool, error)
}

// MemberCheck reports project membership: nil means the groups may access
// project; an apperr-wrapped ErrForbidden/ErrNotFound denies; any other error is
// an infra failure (502). Built from workspace.Service.GetProject at wiring time.
type MemberCheck func(ctx context.Context, project string, groups []string) error

// Guard holds the dependencies RequireProjectRole needs.
type Guard struct {
	roles  ProjectRoleStore
	member MemberCheck
}

// NewGuard builds a project-role guard.
func NewGuard(roles ProjectRoleStore, member MemberCheck) *Guard {
	return &Guard{roles: roles, member: member}
}

// RequireProjectRole gates a handler: the caller must be a member of the {name}
// project AND hold role for it in the store. Membership is checked live, so a DB
// grant goes inert the moment the user leaves the project's developers group.
func (g *Guard) RequireProjectRole(role Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFrom(r.Context())
			if !ok {
				writeForbidden(w, r)
				return
			}
			project := r.PathValue("name")
			if err := g.member(r.Context(), project, u.Groups); err != nil {
				failStatus(w, r, err)
				return
			}
			subject := strings.ToLower(strings.TrimSpace(u.Email))
			has, err := g.roles.HasProjectRole(r.Context(), project, subject, string(role))
			if err != nil {
				failStatus(w, r, err)
				return
			}
			if !has {
				writeForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// failStatus maps a member-check error to a clean status: known apperr classes
// keep their code with a generic message (no internal wording leaked); anything
// else is logged and returned as a 502.
func failStatus(w http.ResponseWriter, r *http.Request, err error) {
	rid := middleware.RequestID(r.Context())
	if st := apperr.Status(err); st != 0 {
		writeJSONError(w, st, strings.ToLower(http.StatusText(st)), rid)
		return
	}
	slog.Error("project-role guard failed", "err", err, "request_id", rid, "path", r.URL.Path)
	writeJSONError(w, http.StatusBadGateway, "upstream service error", rid)
}

func writeJSONError(w http.ResponseWriter, status int, msg, requestID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = jsonEncode(w, map[string]string{"error": msg, "request_id": requestID})
}
