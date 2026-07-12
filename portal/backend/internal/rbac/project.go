package rbac

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/auth"
	"github.com/secured-dev-workspace/developer-portal/internal/middleware"
)

// RoleProjectAdmin can manage MCP servers and member roles within one project.
// It is a DB elevation of a project member, not a Verify group.
const RoleProjectAdmin Role = "project-admin"

// RoleProjectUser is the baseline role every project member carries implicitly
// (it may also be granted explicitly). Like project-admin it is a Portal-DB
// elevation gated by the project's single Verify group — the admin/user
// distinction lives here, not in the IdP.
const RoleProjectUser Role = "project-user"

// Capability names one project-scoped ability a role can carry. What a member
// may DO in a project is the union of their roles' capabilities under the
// project's matrix; membership alone still governs what they can SEE.
type Capability string

const (
	CapWorkspaces Capability = "workspaces"
	CapAIAgents   Capability = "ai-agents"
)

// DefaultCapabilityMatrix is the role→capability mapping a project without a
// stored project_capabilities row uses: both roles get all capabilities. A
// stored matrix replaces it wholesale (e.g. to strip a capability from
// project-user).
func DefaultCapabilityMatrix() map[string][]string {
	return map[string][]string{
		string(RoleProjectAdmin): {string(CapWorkspaces), string(CapAIAgents)},
		string(RoleProjectUser):  {string(CapWorkspaces), string(CapAIAgents)},
	}
}

// ProjectRoleStore is the read side of project-role grants the guard consults
// (satisfied by *store.Memory / *store.Postgres).
type ProjectRoleStore interface {
	HasProjectRole(ctx context.Context, project, subject, role string) (bool, error)
}

// CapabilityStore is the read side RequireCapability consults. Satisfied by a
// thin adapter over store.Store at wiring time so rbac stays store-import-free.
type CapabilityStore interface {
	// RolesForSubjectInProject returns the DB role grants subject holds in project.
	RolesForSubjectInProject(ctx context.Context, project, subject string) ([]string, error)
	// GetCapabilityMatrix returns the project's stored role→capability matrix;
	// ok=false means no row exists and the caller applies DefaultCapabilityMatrix.
	GetCapabilityMatrix(ctx context.Context, project string) (map[string][]string, bool, error)
}

// MemberCheck reports project membership: nil means the groups may access
// project; an apperr-wrapped ErrForbidden/ErrNotFound denies; any other error is
// an infra failure (502). Built from workspace.Service.GetProject at wiring time.
type MemberCheck func(ctx context.Context, project string, groups []string) error

// Guard holds the dependencies RequireProjectRole/RequireCapability need. caps
// may be nil when only role gating is wired (e.g. in tests).
type Guard struct {
	roles  ProjectRoleStore
	caps   CapabilityStore
	member MemberCheck
}

// NewGuard builds a project-role guard.
func NewGuard(roles ProjectRoleStore, caps CapabilityStore, member MemberCheck) *Guard {
	return &Guard{roles: roles, caps: caps, member: member}
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

// RequireCapability gates a handler on a project-scoped capability: the caller
// must be a member of the {name} project AND hold cap under the project's
// role→capability matrix. Every member carries the implicit project-user
// baseline; explicit grants (project-admin / project-user) only add to it.
// Membership is checked live, so grants go inert the moment the user leaves the
// project's developers group.
func (g *Guard) RequireCapability(cap Capability) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := auth.UserFrom(r.Context())
			if !ok || g.caps == nil {
				writeForbidden(w, r)
				return
			}
			project := r.PathValue("name")
			if err := g.member(r.Context(), project, u.Groups); err != nil {
				failStatus(w, r, err)
				return
			}
			subject := strings.ToLower(strings.TrimSpace(u.Email))
			roles, err := g.caps.RolesForSubjectInProject(r.Context(), project, subject)
			if err != nil {
				failStatus(w, r, err)
				return
			}
			roles = append(roles, string(RoleProjectUser))
			matrix, stored, err := g.caps.GetCapabilityMatrix(r.Context(), project)
			if err != nil {
				failStatus(w, r, err)
				return
			}
			if !stored {
				matrix = DefaultCapabilityMatrix()
			}
			if slices.Contains(EffectiveCapabilities(matrix, roles), string(cap)) {
				next.ServeHTTP(w, r)
				return
			}
			writeForbidden(w, r)
		})
	}
}

// EffectiveCapabilities returns the sorted union of the matrix rows for roles.
// A role absent from the matrix contributes nothing (explicit is explicit).
func EffectiveCapabilities(matrix map[string][]string, roles []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, role := range roles {
		for _, c := range matrix[role] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
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
