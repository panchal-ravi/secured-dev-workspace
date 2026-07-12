// Package projectrole manages project-scoped role elevations (currently
// project-admin). Membership stays in IBM Verify; this service records and audits
// the in-app elevation. API-first: handlers are thin adapters over Service.
package projectrole

import (
	"context"
	"errors"
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
	string(rbac.RoleProjectAdmin): true,
	string(rbac.RoleProjectUser):  true,
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
// (platform-admin remains the recovery path). Revoking a project-user is
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

// GetCapabilities returns the project's role→capability matrix and whether it
// is the built-in default (no stored row).
func (s *Service) GetCapabilities(ctx context.Context, project string) (map[string][]string, bool, error) {
	pc, err := s.store.GetProjectCapabilities(ctx, project)
	if errors.Is(err, apperr.ErrNotFound) {
		return rbac.DefaultCapabilityMatrix(), true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return pc.Matrix, false, nil
}

// SetCapabilities replaces the project's matrix wholesale. Every grantable role
// must be present (an omitted role would silently lose all capabilities), only
// known capabilities are accepted, and the project-admin row must keep both
// capabilities so an admin can never lock the admin role out of the planes it
// manages.
func (s *Service) SetCapabilities(ctx context.Context, actor, project string, matrix map[string][]string) error {
	if project == "" {
		return fmt.Errorf("project is required: %w", apperr.ErrBadRequest)
	}
	validCaps := map[string]bool{string(rbac.CapWorkspaces): true, string(rbac.CapAIAgents): true}
	for role, caps := range matrix {
		if !allowed[role] {
			return fmt.Errorf("unknown role %q: %w", role, apperr.ErrBadRequest)
		}
		for _, c := range caps {
			if !validCaps[c] {
				return fmt.Errorf("unknown capability %q: %w", c, apperr.ErrBadRequest)
			}
		}
	}
	for role := range allowed {
		if _, ok := matrix[role]; !ok {
			return fmt.Errorf("matrix must include role %q: %w", role, apperr.ErrBadRequest)
		}
	}
	adminCaps := map[string]bool{}
	for _, c := range matrix[string(rbac.RoleProjectAdmin)] {
		adminCaps[c] = true
	}
	if !adminCaps[string(rbac.CapWorkspaces)] || !adminCaps[string(rbac.CapAIAgents)] {
		return fmt.Errorf("project-admin must retain all capabilities: %w", apperr.ErrBadRequest)
	}
	if _, err := s.store.UpsertProjectCapabilities(ctx, store.ProjectCapabilities{Project: project, Matrix: matrix, CreatedBy: actor}); err != nil {
		return err
	}
	s.audit(ctx, actor, "project-capabilities.set", project, "ok", map[string]any{"matrix": matrix})
	return nil
}

// audit is best-effort: a failed audit write must not fail the mutation, but it
// is logged by the store. Mirrors the admin plane's audit handling.
func (s *Service) audit(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: detail})
}
