// Package codingagentadmin is the platform-admin plane for the coding-agent
// allow-list: which code-known agents (Claude Code, IBM Bob Shell) project-admins
// may pick when creating a workspace flavor. The agent set itself is fixed in code
// (jobtemplate.agents); this plane only persists an enabled flag per agent (absent
// row = enabled). All logic lives in Service (API-first); handlers are thin adapters.
package codingagentadmin

import (
	"context"
	"fmt"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/jobtemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Service manages the coding-agent allow-list on the control-plane store.
type Service struct {
	store store.Store
}

// New builds the service.
func New(st store.Store) *Service { return &Service{store: st} }

// List returns every code-known agent with its resolved enabled state (registry
// order). Absent setting rows resolve to enabled.
func (s *Service) List(ctx context.Context) ([]jobtemplate.Agent, error) {
	settings, err := s.store.ListCodingAgentSettings(ctx)
	if err != nil {
		return nil, err
	}
	return jobtemplate.ResolveAgents(settings), nil
}

// SetEnabled toggles an agent's availability. Unknown keys are rejected so the
// allow-list can never reference a non-existent agent.
func (s *Service) SetEnabled(ctx context.Context, actor, key string, enabled bool) (jobtemplate.Agent, error) {
	if !jobtemplate.KnownAgent(key) {
		return jobtemplate.Agent{}, fmt.Errorf("unknown coding agent %q: %w", key, apperr.ErrNotFound)
	}
	if err := s.store.UpsertCodingAgentSetting(ctx, store.CodingAgentSetting{Key: key, Enabled: enabled}); err != nil {
		s.audit(ctx, actor, key, "error")
		return jobtemplate.Agent{}, err
	}
	s.audit(ctx, actor, key, "ok")
	for _, a := range jobtemplate.ResolveAgents([]store.CodingAgentSetting{{Key: key, Enabled: enabled}}) {
		if a.Key == key {
			return a, nil
		}
	}
	return jobtemplate.Agent{}, nil
}

func (s *Service) audit(ctx context.Context, actor, target, outcome string) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: "coding-agent.set-enabled", Target: target, Outcome: outcome})
}
