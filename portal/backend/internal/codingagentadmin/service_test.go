package codingagentadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestList_DefaultsAllEnabled(t *testing.T) {
	svc := New(store.NewMemory())
	agents, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 code-known agents, got %d", len(agents))
	}
	for _, a := range agents {
		if !a.Enabled {
			t.Fatalf("agent %q should default to enabled", a.Key)
		}
	}
}

func TestSetEnabled_TogglesAndValidates(t *testing.T) {
	svc := New(store.NewMemory())
	ctx := context.Background()

	a, err := svc.SetEnabled(ctx, "admin@x", "bob", false)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if a.Key != "bob" || a.Enabled {
		t.Fatalf("returned agent wrong: %+v", a)
	}

	agents, _ := svc.List(ctx)
	for _, ag := range agents {
		if ag.Key == "bob" && ag.Enabled {
			t.Fatalf("bob should now be disabled: %+v", agents)
		}
	}

	// Re-enable round-trips.
	if _, err := svc.SetEnabled(ctx, "admin@x", "bob", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	// Unknown agent rejected.
	if _, err := svc.SetEnabled(ctx, "admin@x", "grok", true); !errors.Is(err, apperr.ErrNotFound) {
		t.Fatalf("unknown agent: err = %v, want ErrNotFound", err)
	}
}
