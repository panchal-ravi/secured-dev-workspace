// Package projectrole manages project-scoped role elevations (currently
// project-admin). Membership stays in IBM Verify; this service records and audits
// the in-app elevation. API-first: handlers are thin adapters over Service.
package projectrole

import (
	"context"
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/rbac"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Service grants, revokes, and lists project roles over the store.
type Service struct {
	store store.Store
}

// New builds the project-role service.
func New(st store.Store) *Service { return &Service{store: st} }

// allowed is the set of roles a caller may grant. Kept explicit so a typo can
// never mint an unexpected role.
var allowed = map[string]bool{
	string(rbac.RoleProjectAdmin):     true,
	string(rbac.RoleProjectDeveloper): true,
}

func normalize(subject string) string { return strings.ToLower(strings.TrimSpace(subject)) }

// Grant elevates subject to role in project. Used by both the platform-admin
// bootstrap route and the project-admin self-service route — the gate differs at
// the mux, the logic is identical. Idempotent.
func (s *Service) Grant(ctx context.Context, actor, project, subject, role string) (store.ProjectRole, error) {
	subject = normalize(subject)
	if project == "" || subject == "" {
		return store.ProjectRole{}, fmt.Errorf("project and subject are required: %w", apperr.ErrBadRequest)
	}
	if !allowed[role] {
		return store.ProjectRole{}, fmt.Errorf("unknown role %q: %w", role, apperr.ErrBadRequest)
	}
	pr, err := s.store.GrantProjectRole(ctx, store.ProjectRole{Project: project, Subject: subject, Role: role, GrantedBy: actor})
	if err != nil {
		return store.ProjectRole{}, err
	}
	s.audit(ctx, actor, "project-role.grant", project, "ok", map[string]any{"subject": subject, "role": role})
	return pr, nil
}

// Revoke removes a (subject, role) grant. An empty role defaults to project-admin
// for backward compatibility. The last-admin guard applies only to project-admin:
// it refuses a revoke that would leave the project with zero project-admins
// (platform-admin remains the recovery path). Revoking a project-developer is
// never guarded.
func (s *Service) Revoke(ctx context.Context, actor, project, subject, role string) error {
	subject = normalize(subject)
	if role == "" {
		role = string(rbac.RoleProjectAdmin)
	}
	if !allowed[role] {
		return fmt.Errorf("unknown role %q: %w", role, apperr.ErrBadRequest)
	}
	if role == string(rbac.RoleProjectAdmin) {
		admins, err := s.store.ListProjectRoles(ctx, project)
		if err != nil {
			return err
		}
		count, isAdmin := 0, false
		for _, pr := range admins {
			if pr.Role == role {
				count++
				if pr.Subject == subject {
					isAdmin = true
				}
			}
		}
		if isAdmin && count <= 1 {
			return fmt.Errorf("cannot remove the last project-admin: %w", apperr.ErrConflict)
		}
	}
	if err := s.store.RevokeProjectRole(ctx, project, subject, role); err != nil {
		return err
	}
	s.audit(ctx, actor, "project-role.revoke", project, "ok", map[string]any{"subject": subject, "role": role})
	return nil
}

// List returns the role grants in a project (for the Members page).
func (s *Service) List(ctx context.Context, project string) ([]store.ProjectRole, error) {
	return s.store.ListProjectRoles(ctx, project)
}

// ForSubject returns every grant held by subject (for /api/me).
func (s *Service) ForSubject(ctx context.Context, subject string) ([]store.ProjectRole, error) {
	return s.store.ProjectRolesForSubject(ctx, normalize(subject))
}

// audit is best-effort: a failed audit write must not fail the mutation, but it
// is logged by the store. Mirrors the admin plane's audit handling.
func (s *Service) audit(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: detail})
}
