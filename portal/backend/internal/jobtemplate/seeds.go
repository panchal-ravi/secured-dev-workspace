package jobtemplate

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

//go:embed seeds/dev-workspace.nomad.hcl seeds/gpu-workspace.nomad.hcl seeds/microvm-workspace.nomad.hcl seeds/bobshell-workspace.nomad.hcl
var seedFS embed.FS

// seedMeta is the non-source metadata for a base template: picker label, node
// placement, runtime, and the feature cards. It mirrors the per-flavor data that
// terraform/project/portal.tf carried in its flavor_features map.
type seedMeta struct {
	file            string
	label           string
	description     string
	image           string // container image baked into project templates (portal-admin owned)
	defaultNodePool string
	runtime         string
	codingAgent     string // "claude" (default) | "bob"; drives how MCP is wired into the workspace
	features        []store.Feature
}

var claudeFeature = store.Feature{
	Key:         "claude-deepseek",
	Label:       "Claude Code CLI (governed model)",
	Description: "Pre-configured AI coding assistant. The API key is injected per session from Vault and never lands on the persistent home volume.",
}

var gitPATFeature = store.Feature{
	Key:         "git-dynamic-pat",
	Label:       "Git push (dynamic PAT)",
	Description: "git is pre-configured with your identity and a short-lived GitHub App token as the push credential — no static PAT anywhere.",
}

// bobFeature is the coding-agent card for the IBM Bob Shell template. Its
// description states the governance caveat plainly: unlike Claude Code, Bob's LLM
// runs on IBM's hosted backend, NOT the governed LiteLLM gateway — so no per-project
// key, budget, or guardrail applies to its model traffic. MCP, git, and shared
// volumes still work as usual. You sign in interactively with your own IBMid the
// first time you run `bob` in the workspace (per-user identity; no shared key).
var bobFeature = store.Feature{
	Key:   "bob-shell-ibm-hosted",
	Label: "IBM Bob Shell CLI (IBM-hosted model)",
	Description: "Pre-configured IBM Bob Shell coding agent. Sign in with your IBMid the first " +
		"time you run `bob`. NOTE: Bob's LLM runs on IBM's hosted backend, NOT the governed " +
		"LiteLLM gateway — its model traffic is outside per-project keys, budgets, and guardrails.",
}

// seeds is the canonical set of base templates the portal ships with.
var seeds = map[string]seedMeta{
	"dev-workspace": {
		file:        "seeds/dev-workspace.nomad.hcl",
		label:       "Standard Dev Workspace",
		description: "Full dev environment: Claude Code (governed model), dynamic Git PAT.",
		image:       "panchalravi/dev-workspace:poc",
		codingAgent: "claude",
		features:    []store.Feature{claudeFeature, gitPATFeature},
	},
	"gpu-workspace": {
		file:            "seeds/gpu-workspace.nomad.hcl",
		label:           "GPU Workspace (NVIDIA T4)",
		description:     "Everything in the standard workspace plus a CUDA toolchain on an NVIDIA T4 GPU.",
		image:           "panchalravi/gpu-workspace:poc",
		defaultNodePool: "gpu",
		runtime:         "nvidia",
		codingAgent:     "claude",
		features: []store.Feature{
			{Key: "nvidia-t4-gpu", Label: "NVIDIA T4 GPU", Description: "Scheduled on a GPU node (g4dn.xlarge, NVIDIA T4). nvidia-smi and nvcc are available; a CUDA vectorAdd sample is included in the repo."},
			claudeFeature, gitPATFeature,
		},
	},
	"microvm-workspace": {
		file:            "seeds/microvm-workspace.nomad.hcl",
		label:           "Hardened Workspace (microVM)",
		description:     "Everything in the standard workspace, isolated in a Kata microVM (separate guest kernel) on a bare-metal node.",
		image:           "panchalravi/dev-workspace:poc",
		defaultNodePool: "microvm",
		runtime:         "kata",
		codingAgent:     "claude",
		features: []store.Feature{
			{Key: "kata-microvm-isolation", Label: "Hardware-isolated microVM", Description: "Runs inside a Kata Containers microVM with its own guest kernel on a dedicated bare-metal node — a hardware-virtualization (KVM) boundary around AI-agent code, not just shared-kernel namespaces."},
			claudeFeature, gitPATFeature,
		},
	},
	"bobshell-workspace": {
		file:        "seeds/bobshell-workspace.nomad.hcl",
		label:       "IBM Bob Shell Workspace",
		description: "Dev environment with the IBM Bob Shell coding agent (IBM-hosted model) instead of Claude Code, dynamic Git PAT.",
		image:       "panchalravi/bobshell-workspace:poc",
		codingAgent: "bob",
		features:    []store.Feature{bobFeature, gitPATFeature},
	},
}

// HashSource is the content hash stamped on a published template (drift detection).
func HashSource(src string) string {
	sum := sha256.Sum256([]byte(src))
	return hex.EncodeToString(sum[:])
}

// SeedBaseTemplates inserts the shipped base templates that are not already in the
// store, as published v1. It NEVER overwrites an existing row, so a portal-admin's
// edits survive a restart. Idempotent.
func SeedBaseTemplates(ctx context.Context, st store.Store) error {
	for name, meta := range seeds {
		if _, err := st.GetBaseJobTemplate(ctx, name); err == nil {
			continue // already present (possibly admin-edited) — leave it
		} else if !errors.Is(err, apperr.ErrNotFound) {
			return fmt.Errorf("jobtemplate: check %q: %w", name, err)
		}
		src, err := seedFS.ReadFile(meta.file)
		if err != nil {
			return fmt.Errorf("jobtemplate: read seed %q: %w", meta.file, err)
		}
		if err := ValidatePlaceholders(string(src)); err != nil {
			return fmt.Errorf("jobtemplate: seed %q: %w", name, err)
		}
		if _, err := st.UpsertBaseJobTemplate(ctx, store.BaseJobTemplate{
			Name:            name,
			Label:           meta.label,
			Description:     meta.description,
			Status:          store.StatusPublished,
			Version:         1,
			ContentHash:     HashSource(string(src)),
			DraftSource:     string(src),
			PublishedSource: string(src),
			Image:           meta.image,
			Features:        meta.features,
			DefaultNodePool: meta.defaultNodePool,
			Runtime:         meta.runtime,
			CodingAgent:     meta.codingAgent,
			CreatedBy:       "seed",
		}); err != nil {
			return fmt.Errorf("jobtemplate: seed %q: %w", name, err)
		}
	}
	return nil
}
