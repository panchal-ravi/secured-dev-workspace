package basetmpladmin

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/jobtemplate"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func seed(t *testing.T) (*Service, store.Store) {
	t.Helper()
	st := store.NewMemory()
	if err := jobtemplate.SeedBaseTemplates(context.Background(), st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return New(st), st
}

func TestUpdateDraftValidatesAndMarksDraft(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()

	// A good edit (only known placeholders) succeeds and flips status to draft
	// without touching the published source/version.
	before, _ := s.Get(ctx, "dev-workspace")
	good := `job "${job_name}" { namespace = "${namespace}" }`
	got, err := s.UpdateDraft(ctx, "admin@x", "dev-workspace", good)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Status != store.StatusDraft || got.DraftSource != good {
		t.Fatalf("draft not saved/marked: %+v", got)
	}
	if got.PublishedSource != before.PublishedSource || got.Version != before.Version {
		t.Fatalf("published source/version disturbed by a draft edit: %+v", got)
	}

	// An unknown placeholder is rejected.
	if _, err := s.UpdateDraft(ctx, "admin@x", "dev-workspace", `${bogus}`); !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("want ErrBadRequest for unknown placeholder, got %v", err)
	}
}

func TestPublishBumpsVersionAndFreezesSource(t *testing.T) {
	s, _ := seed(t)
	ctx := context.Background()

	edited := `job "${job_name}" { namespace = "${namespace}" image = "${image}" }`
	if _, err := s.UpdateDraft(ctx, "admin@x", "dev-workspace", edited); err != nil {
		t.Fatalf("update: %v", err)
	}
	pub, err := s.Publish(ctx, "admin@x", "dev-workspace")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if pub.Status != store.StatusPublished || pub.Version != 2 || pub.PublishedSource != edited {
		t.Fatalf("publish did not freeze+bump: %+v", pub)
	}
	if pub.ContentHash != jobtemplate.HashSource(edited) {
		t.Fatalf("content hash not stamped: %+v", pub)
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	s, _ := seed(t)
	if _, err := s.Get(context.Background(), "nope"); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
