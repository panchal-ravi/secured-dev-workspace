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

//go:embed seeds/dev-workspace.nomad.hcl seeds/gpu-workspace.nomad.hcl seeds/microvm-workspace.nomad.hcl
var seedFS embed.FS

// seedMeta is the non-source metadata for a base template: picker label, node
// placement, runtime, and the INFRA feature cards. Base templates are agent-agnostic
// — the coding agent is an orthogonal dimension chosen at flavor create (see
// agents.go), so no coding-agent metadata lives here.
type seedMeta struct {
	file            string
	label           string
	description     string
	image           string // container image baked into project templates (portal-admin owned)
	defaultNodePool string
	runtime         string
	features        []store.Feature
}

var gitPATFeature = store.Feature{
	Key:         "git-dynamic-pat",
	Label:       "Git push (dynamic PAT)",
	Description: "git is pre-configured with your identity and a short-lived GitHub App token as the push credential — no static PAT anywhere.",
}

// seeds is the canonical set of base templates the portal ships with — the three
// INFRA flavors (standard / GPU / microVM). The workspace image bakes every coding
// agent's binary, so a flavor selects its agent at create time (agents.go) rather
// than the base template carrying one.
var seeds = map[string]seedMeta{
	"dev-workspace": {
		file:        "seeds/dev-workspace.nomad.hcl",
		label:       "Standard Dev Workspace",
		description: "Full dev environment on a standard node, with a dynamic Git PAT. Pick the coding agent at flavor create.",
		image:       "panchalravi/workspace-base:poc",
		features:    []store.Feature{gitPATFeature},
	},
	"gpu-workspace": {
		file:            "seeds/gpu-workspace.nomad.hcl",
		label:           "GPU Workspace (NVIDIA T4)",
		description:     "Everything in the standard workspace plus a CUDA toolchain on an NVIDIA T4 GPU.",
		image:           "panchalravi/gpu-workspace:poc",
		defaultNodePool: "gpu",
		runtime:         "nvidia",
		features: []store.Feature{
			{Key: "nvidia-t4-gpu", Label: "NVIDIA T4 GPU", Description: "Scheduled on a GPU node (g4dn.xlarge, NVIDIA T4). nvidia-smi and nvcc are available; a CUDA vectorAdd sample is included in the repo."},
			gitPATFeature,
		},
	},
	"microvm-workspace": {
		file:            "seeds/microvm-workspace.nomad.hcl",
		label:           "Hardened Workspace (microVM)",
		description:     "Everything in the standard workspace, isolated in a Kata microVM (separate guest kernel) on a bare-metal node.",
		image:           "panchalravi/workspace-base:poc",
		defaultNodePool: "microvm",
		runtime:         "kata",
		features: []store.Feature{
			{Key: "kata-microvm-isolation", Label: "Hardware-isolated microVM", Description: "Runs inside a Kata Containers microVM with its own guest kernel on a dedicated bare-metal node — a hardware-virtualization (KVM) boundary around AI-agent code, not just shared-kernel namespaces."},
			gitPATFeature,
		},
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
			CreatedBy:       "seed",
		}); err != nil {
			return fmt.Errorf("jobtemplate: seed %q: %w", name, err)
		}
	}
	return nil
}
