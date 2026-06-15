//go:build live
// +build live

// Live, gated integration harness for the seed blueprints. It is excluded from the
// normal build (build tag `live`) and additionally skips unless BLUEPRINT_LIVE_VAULT=1
// is set, so CI stays hermetic. Run it only against a reachable Vault Enterprise (the
// same one the project tier uses), exactly the gated way the nstest/acme live work was
// run:
//
//	BLUEPRINT_LIVE_VAULT=1 VAULT_ADDR=... VAULT_TOKEN=... VAULT_SKIP_VERIFY=true \
//	  go test -tags live ./internal/blueprint/ -run Live -v
//
// For each seed manifest it constructs a real VaultAdmin, runs Validator.Validate
// (which creates a throwaway namespace, instantiates, probes allowed-200/denied-403,
// then deprovisions + deletes the namespace), and asserts result.Passed. Afterwards no
// bp-validate-* namespace should remain (verify with `vault namespace list`).
//
// Caveat for Class A (postgres-mcp): the synthetic connection_url points at a
// non-routable address, so the immediate rotate-root step will fail unless a reachable
// Postgres is supplied. When validating Class A live, point the seed (or the harness)
// at a throwaway Postgres reachable from Vault.
package blueprint_test

import (
	"context"
	"os"
	"testing"
	"time"

	vapi "github.com/hashicorp/vault/api"

	"github.com/secured-dev-workspace/developer-portal/internal/blueprint"
)

func TestLiveSeedValidation(t *testing.T) {
	if os.Getenv("BLUEPRINT_LIVE_VAULT") != "1" {
		t.Skip("set BLUEPRINT_LIVE_VAULT=1 (and VAULT_ADDR/VAULT_TOKEN) to run the live seed validation")
	}

	cfg := vapi.DefaultConfig() // reads VAULT_ADDR / VAULT_SKIP_VERIFY from the env
	client, err := vapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault client: %v", err)
	}
	if tok := os.Getenv("VAULT_TOKEN"); tok != "" {
		client.SetToken(tok)
	}

	vadmin := blueprint.NewVaultAdmin(client)
	executor := blueprint.NewExecutor(vadmin, blueprint.ExecutorConfig{
		AuthPath:      "jwt-nomad",
		BoundAudience: "vault",
		KVMount:       "secret",
	})
	validator := blueprint.NewValidator(vadmin, executor)

	seeds, err := blueprint.SeedManifests()
	if err != nil {
		t.Fatalf("load seeds: %v", err)
	}
	if len(seeds) == 0 {
		t.Fatal("no seed manifests embedded")
	}

	for _, m := range seeds {
		m := m
		t.Run(m.ID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := validator.Validate(ctx, m)
			if err != nil {
				t.Fatalf("%s: validate transport error: %v", m.ID, err)
			}
			if !res.Passed {
				t.Fatalf("%s: validation did not pass: %s; checks=%+v", m.ID, res.Message, res.Checks)
			}
		})
	}
}
