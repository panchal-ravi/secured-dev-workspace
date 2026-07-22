package jobtemplate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

func TestValidatePlaceholders(t *testing.T) {
	ok := `job "${job_name}" { namespace = "${namespace}" image = "${image}" }`
	if err := ValidatePlaceholders(ok); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	// consul-template {{ }} and bash $(...) must not be treated as placeholders.
	if err := ValidatePlaceholders(`{{ with secret "x" }}{{ .Data.k }}{{ end }} $(cat /x) ${job_name}`); err != nil {
		t.Fatalf("consul-template/bash tripped the validator: %v", err)
	}
	err := ValidatePlaceholders(`job "${job_name}" { x = "${ssh_prt}" }`)
	if !errors.Is(err, apperr.ErrBadRequest) || !strings.Contains(err.Error(), "${ssh_prt}") {
		t.Fatalf("want ErrBadRequest naming ${ssh_prt}, got %v", err)
	}
}

func TestSeedsAreValidAndComplete(t *testing.T) {
	// Every shipped seed must reference only known placeholders and must include the
	// full project-static + per-workspace set EXCEPT the tokens that are INJECTED
	// rather than baked into the agent-agnostic base body: mcp_kv_path (add-on MCP
	// wiring) and the llm_* tokens (Claude Code's governed-gateway wiring, emitted by
	// Inject based on the agent chosen at flavor create).
	notInBaseBody := map[string]bool{
		"mcp_kv_path":       true,
		"llm_kv_path":       true,
		"llm_base_url":      true,
		"llm_model_primary": true,
		"llm_model_fast":    true,
	}
	for name, meta := range seeds {
		src, err := seedFS.ReadFile(meta.file)
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		if err := ValidatePlaceholders(string(src)); err != nil {
			t.Fatalf("seed %q invalid: %v", name, err)
		}
		s := string(src)
		for _, ph := range append(append([]string{}, ProjectStaticPlaceholders...), PerWorkspacePlaceholders...) {
			if notInBaseBody[ph] {
				// Injected, not baked — the agent-agnostic base must NOT reference it.
				if strings.Contains(s, "${"+ph+"}") {
					t.Errorf("agent-agnostic seed %q should not reference injected placeholder ${%s}", name, ph)
				}
				continue
			}
			if !strings.Contains(s, "${"+ph+"}") {
				t.Errorf("seed %q missing placeholder ${%s}", name, ph)
			}
		}
	}
}

func TestSeedBaseTemplatesIdempotentAndNonDestructive(t *testing.T) {
	st := store.NewMemory()
	ctx := context.Background()
	if err := SeedBaseTemplates(ctx, st); err != nil {
		t.Fatalf("seed: %v", err)
	}
	list, _ := st.ListBaseJobTemplates(ctx)
	if len(list) != 3 {
		t.Fatalf("want 3 seeded (infra-only) templates, got %d", len(list))
	}
	dev, err := st.GetBaseJobTemplate(ctx, "dev-workspace")
	if err != nil || dev.Status != store.StatusPublished || dev.Version != 1 || dev.ContentHash == "" {
		t.Fatalf("seeded dev-workspace wrong: %+v err=%v", dev, err)
	}
	// Base templates are agent-agnostic now — no coding agent baked into them.
	if dev.CodingAgent != "" {
		t.Fatalf("dev-workspace base should carry no coding agent, got %q", dev.CodingAgent)
	}
	if dev.Image != "panchalravi/workspace-base:poc" {
		t.Fatalf("dev-workspace should use the universal image, got %q", dev.Image)
	}
	// The old agent-specific base is gone (Standard base + Bob agent replaces it).
	if _, err := st.GetBaseJobTemplate(ctx, "bobshell-workspace"); err == nil {
		t.Fatalf("bobshell-workspace base should no longer be seeded")
	}

	// Simulate an admin edit, then re-seed: the edit must survive.
	dev.DraftSource = "edited"
	dev.Version = 5
	if _, err := st.UpsertBaseJobTemplate(ctx, dev); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := SeedBaseTemplates(ctx, st); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	got, _ := st.GetBaseJobTemplate(ctx, "dev-workspace")
	if got.DraftSource != "edited" || got.Version != 5 {
		t.Fatalf("re-seed clobbered an admin edit: %+v", got)
	}
}
