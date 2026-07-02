// Package basetmpladmin is the platform-admin plane for base Nomad job templates:
// the generic standard/GPU/microVM templates, seeded into the store and editable
// here. Templates are MUTABLE with a version that bumps on publish; project
// templates snapshot the published source at create time, so an edit here never
// disturbs live projects. All logic lives in Service (API-first); handlers are thin
// adapters. Every mutation writes an audit event.
package basetmpladmin

import (
	"context"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/jobtemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// Service manages base job templates on the Postgres control-plane store.
type Service struct {
	store store.Store
}

// New builds the service.
func New(st store.Store) *Service { return &Service{store: st} }

// List returns all base templates (portal-admin view).
func (s *Service) List(ctx context.Context) ([]store.BaseJobTemplate, error) {
	return s.store.ListBaseJobTemplates(ctx)
}

// Get returns one base template by name.
func (s *Service) Get(ctx context.Context, name string) (store.BaseJobTemplate, error) {
	return s.store.GetBaseJobTemplate(ctx, name)
}

// UpdateDraft validates and saves the editable draft source + the container image,
// marking the template as having pending changes (status=draft). The published
// source and version are untouched, so projects deriving from the last publish are
// unaffected. The image is a template property (not versioned) that project
// templates bake in; a project-admin never supplies it.
func (s *Service) UpdateDraft(ctx context.Context, actor, name, source, image string) (store.BaseJobTemplate, error) {
	source = strings.TrimSpace(source)
	if err := jobtemplate.ValidatePlaceholders(source); err != nil {
		s.audit(ctx, actor, "base-template.update", name, "error")
		return store.BaseJobTemplate{}, err
	}
	t, err := s.store.GetBaseJobTemplate(ctx, name)
	if err != nil {
		return store.BaseJobTemplate{}, err
	}
	t.DraftSource = source
	t.Image = strings.TrimSpace(image)
	t.Status = store.StatusDraft
	saved, err := s.store.UpsertBaseJobTemplate(ctx, t)
	if err != nil {
		return store.BaseJobTemplate{}, err
	}
	s.audit(ctx, actor, "base-template.update", name, "ok")
	return saved, nil
}

// Publish freezes the draft as the published source, bumps the version, and stamps
// the content hash. Re-validates first (the draft could have been saved before a
// rule change). Idempotent-safe: publishing an unchanged draft still bumps the
// version, which is the explicit "cut a new release" action.
func (s *Service) Publish(ctx context.Context, actor, name string) (store.BaseJobTemplate, error) {
	t, err := s.store.GetBaseJobTemplate(ctx, name)
	if err != nil {
		return store.BaseJobTemplate{}, err
	}
	if err := jobtemplate.ValidatePlaceholders(t.DraftSource); err != nil {
		s.audit(ctx, actor, "base-template.publish", name, "error")
		return store.BaseJobTemplate{}, err
	}
	t.PublishedSource = t.DraftSource
	t.Version++
	t.ContentHash = jobtemplate.HashSource(t.PublishedSource)
	t.Status = store.StatusPublished
	saved, err := s.store.UpsertBaseJobTemplate(ctx, t)
	if err != nil {
		return store.BaseJobTemplate{}, err
	}
	s.audit(ctx, actor, "base-template.publish", name, "ok")
	return saved, nil
}

func (s *Service) audit(ctx context.Context, actor, action, target, outcome string) {
	_ = s.store.AppendAudit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Outcome: outcome})
}
