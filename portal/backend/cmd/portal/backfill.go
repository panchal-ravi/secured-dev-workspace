package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/secured-dev-workspace/developer-portal/internal/config"
	"github.com/secured-dev-workspace/developer-portal/internal/descriptor"
	"github.com/secured-dev-workspace/developer-portal/internal/hashistack"
	"github.com/secured-dev-workspace/developer-portal/internal/store"
)

// runBackfill is a one-shot migration: it copies each project's portal descriptor
// from Vault KV (secret/data/projects/<p>/portal-descriptor) into the Postgres
// control-plane store (project_descriptors). Idempotent — re-running upserts the
// same rows. Invoke as `portal backfill-from-vault`; requires PORTAL_DB_DSN and the
// usual PORTAL_VAULT_* env. Job-template rows are backfilled in Phase B.
func runBackfill() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.DBDSN == "" {
		return fmt.Errorf("backfill requires PORTAL_DB_DSN (the in-memory store is not durable)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	st, err := store.NewPostgres(ctx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer st.Close()

	tls := hashistack.TLSOptions{CACertPath: cfg.HashiCACertPath, SkipVerify: cfg.TLSSkipVerify}
	vault, err := hashistack.NewVault(cfg.VaultAddr, cfg.VaultToken, cfg.VaultTokenFile, cfg.VaultKVMount, tls)
	if err != nil {
		return err
	}
	cl := vault.APIClient()
	mount := cfg.VaultKVMount

	// Enumerate projects from the KV-v2 metadata index (the pre-migration source).
	sec, err := cl.Logical().ListWithContext(ctx, mount+"/metadata/projects")
	if err != nil {
		return fmt.Errorf("backfill: list projects: %w", err)
	}
	names := []string{}
	if sec != nil && sec.Data != nil {
		if raw, ok := sec.Data["keys"].([]any); ok {
			for _, k := range raw {
				if s, ok := k.(string); ok {
					names = append(names, strings.TrimSuffix(s, "/"))
				}
			}
		}
	}

	migrated := 0
	for _, p := range names {
		ds, err := cl.KVv2(mount).Get(ctx, "projects/"+p+"/portal-descriptor")
		if err != nil {
			slog.Warn("backfill: skip project (no descriptor)", "project", p, "err", err)
			continue
		}
		rawDesc, ok := ds.Data["descriptor"].(string)
		if !ok {
			slog.Warn("backfill: skip project (missing 'descriptor' key)", "project", p)
			continue
		}
		if _, err := descriptor.Parse(rawDesc); err != nil {
			slog.Warn("backfill: skip project (malformed descriptor)", "project", p, "err", err)
			continue
		}
		if _, err := st.UpsertProjectDescriptor(ctx, store.ProjectDescriptor{
			Project:    p,
			Status:     store.StatusReady,
			Descriptor: json.RawMessage(rawDesc),
			CreatedBy:  "backfill",
		}); err != nil {
			return fmt.Errorf("backfill: upsert %q: %w", p, err)
		}
		migrated++
		slog.Info("backfill: migrated descriptor", "project", p)
	}
	slog.Info("backfill complete", "projects_seen", len(names), "migrated", migrated)
	return nil
}
