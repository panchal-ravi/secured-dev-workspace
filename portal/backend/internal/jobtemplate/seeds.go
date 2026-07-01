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
// placement, runtime, and the feature cards. It mirrors the per-flavor data that
// terraform/project/portal.tf carried in its flavor_features map.
type seedMeta struct {
	file            string
	label           string
	description     string
	defaultNodePool string
	runtime         string
	features        []store.Feature
}

var claudeFeature = store.Feature{
	Key:         "claude-deepseek",
	Label:       "Claude Code CLI (governed model)",
	Description: "Pre-configured AI coding assistant. The API key is injected per session from Vault and never lands on the persistent home volume.",
}

var dbMCPFeature = store.Feature{
	Key:         "db-mcp-readonly",
	Label:       "Database MCP (read-only)",
	Description: "A read-only Postgres MCP server, federated through the central ContextForge MCP gateway as this project's virtual MCP server. Backed by a Vault-dynamic, read-only database credential.",
}

var gitPATFeature = store.Feature{
	Key:         "git-dynamic-pat",
	Label:       "Git push (dynamic PAT)",
	Description: "git is pre-configured with your identity and a short-lived GitHub App token as the push credential — no static PAT anywhere.",
}

// seeds is the canonical set of base templates the portal ships with.
var seeds = map[string]seedMeta{
	"dev-workspace": {
		file:        "seeds/dev-workspace.nomad.hcl",
		label:       "Standard Dev Workspace",
		description: "Full dev environment: Claude Code (governed model), read-only DB MCP, dynamic Git PAT.",
		features:    []store.Feature{claudeFeature, dbMCPFeature, gitPATFeature},
	},
	"gpu-workspace": {
		file:            "seeds/gpu-workspace.nomad.hcl",
		label:           "GPU Workspace (NVIDIA T4)",
		description:     "Everything in the standard workspace plus a CUDA toolchain on an NVIDIA T4 GPU.",
		defaultNodePool: "gpu",
		runtime:         "nvidia",
		features: []store.Feature{
			{Key: "nvidia-t4-gpu", Label: "NVIDIA T4 GPU", Description: "Scheduled on a GPU node (g4dn.xlarge, NVIDIA T4). nvidia-smi and nvcc are available; a CUDA vectorAdd sample is included in the repo."},
			claudeFeature, dbMCPFeature, gitPATFeature,
		},
	},
	"microvm-workspace": {
		file:            "seeds/microvm-workspace.nomad.hcl",
		label:           "Hardened Workspace (microVM)",
		description:     "Everything in the standard workspace, isolated in a Kata microVM (separate guest kernel) on a bare-metal node.",
		defaultNodePool: "microvm",
		runtime:         "kata",
		features: []store.Feature{
			{Key: "kata-microvm-isolation", Label: "Hardware-isolated microVM", Description: "Runs inside a Kata Containers microVM with its own guest kernel on a dedicated bare-metal node — a hardware-virtualization (KVM) boundary around AI-agent code, not just shared-kernel namespaces."},
			claudeFeature, dbMCPFeature, gitPATFeature,
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
