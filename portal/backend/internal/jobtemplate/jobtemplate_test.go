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
	// Every shipped seed must reference only known placeholders and must include
	// the full set EXCEPT mcp_kv_path, which is reserved for add-on injection
	// (C-R.6) — the base body carries no MCP wiring. The llm_* placeholders wire the
	// governed LiteLLM gateway and are Claude-agent-only; the IBM Bob Shell seed
	// intentionally omits them (Bob's model is IBM-hosted, not gateway-governed).
	notInBaseBody := map[string]bool{"mcp_kv_path": true}
	llmOnly := map[string]bool{"llm_kv_path": true, "llm_base_url": true, "llm_model_primary": true, "llm_model_fast": true}
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
				continue
			}
			if meta.codingAgent == "bob" && llmOnly[ph] {
				// The Bob seed must NOT carry LLM wiring — assert its absence.
				if strings.Contains(s, "${"+ph+"}") {
					t.Errorf("bob seed %q should not reference LLM placeholder ${%s}", name, ph)
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
	if len(list) != 4 {
		t.Fatalf("want 4 seeded templates, got %d", len(list))
	}
	dev, err := st.GetBaseJobTemplate(ctx, "dev-workspace")
	if err != nil || dev.Status != store.StatusPublished || dev.Version != 1 || dev.ContentHash == "" {
		t.Fatalf("seeded dev-workspace wrong: %+v err=%v", dev, err)
	}
	if dev.CodingAgent != "claude" {
		t.Fatalf("dev-workspace should be a claude agent, got %q", dev.CodingAgent)
	}
	bob, err := st.GetBaseJobTemplate(ctx, "bobshell-workspace")
	if err != nil || bob.CodingAgent != "bob" || bob.Image != "panchalravi/bobshell-workspace:poc" {
		t.Fatalf("seeded bobshell-workspace wrong: %+v err=%v", bob, err)
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
