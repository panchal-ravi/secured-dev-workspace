package projectbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	vapi "github.com/hashicorp/vault/api"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

func writeJWT(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nomad_vault_creator.jwt")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("write jwt: %v", err)
	}
	return p
}

func newClient(t *testing.T, addr string) *vapi.Client {
	t.Helper()
	cfg := vapi.DefaultConfig()
	cfg.Address = addr
	c, err := vapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("vapi client: %v", err)
	}
	c.SetToken("ROOT-TOKEN-MUST-NOT-BE-USED")
	return c
}

// capture records the namespace + token headers a request carried.
type capture struct{ ns, token string }

// TestCreatorBrokersAndSeeds proves the creator logs in once (cached), creates the
// namespace in the ROOT namespace, and performs the child-namespace seeds under
// the BROKERED token + child namespace header — never the shared root token.
func TestCreatorBrokersAndSeeds(t *testing.T) {
	var loginCalls int
	got := map[string]capture{}
	record := func(key string, r *http.Request) {
		got[key] = capture{ns: r.Header.Get("X-Vault-Namespace"), token: r.Header.Get("X-Vault-Token")}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/jwt-nomad/login", func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{"client_token": "creator-tok", "lease_duration": 300},
		})
	})
	mux.HandleFunc("/v1/sys/namespaces/project-acme", func(w http.ResponseWriter, r *http.Request) {
		record("ns-create", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/sys/auth/jwt-nomad", func(w http.ResponseWriter, r *http.Request) {
		record("enable", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/jwt-nomad/config", func(w http.ResponseWriter, r *http.Request) {
		record("config", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/sys/mounts/secret", func(w http.ResponseWriter, r *http.Request) {
		record("kv", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/sys/policies/acl/portal-provisioner", func(w http.ResponseWriter, r *http.Request) {
		record("policy", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/jwt-nomad/role/portal-provisioner", func(w http.ResponseWriter, r *http.Request) {
		record("prov-role", r)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/auth/jwt-nomad/role/project-acme", func(w http.ResponseWriter, r *http.Request) {
		record("ws-role", r)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newClient(t, srv.URL)
	vc := NewVaultCreator(c, CreatorConfig{JWTPath: writeJWT(t, "the-jwt"), Role: "project-creator", AuthMount: "jwt-nomad"},
		JWKSConfig{URL: "https://127.0.0.1:4646/.well-known/jwks.json", CAPEM: "ca"})
	ctx := context.Background()

	for _, step := range []struct {
		name string
		fn   func() error
	}{
		{"CreateNamespace", func() error { return vc.CreateNamespace(ctx, "project-acme") }},
		{"EnableJWTNomad", func() error { return vc.EnableJWTNomad(ctx, "project-acme") }},
		{"MountSecretKV", func() error { return vc.MountSecretKV(ctx, "project-acme") }},
		{"SeedProvisioner", func() error { return vc.SeedProvisioner(ctx, "project-acme") }},
		{"SeedWorkspaceRole", func() error { return vc.SeedWorkspaceRole(ctx, "project-acme", "project-acme") }},
	} {
		if err := step.fn(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1 (token should be cached)", loginCalls)
	}
	// Namespace creation is a ROOT-namespace op: no namespace header, brokered token.
	if c := got["ns-create"]; c.ns != "" || c.token != "creator-tok" {
		t.Fatalf("ns-create ns=%q token=%q, want ns=\"\" token=creator-tok", c.ns, c.token)
	}
	// Every child seed must carry the child namespace + brokered token.
	for _, key := range []string{"enable", "config", "kv", "policy", "prov-role", "ws-role"} {
		c := got[key]
		if c.ns != "project-acme" {
			t.Fatalf("%s namespace = %q, want project-acme", key, c.ns)
		}
		if c.token != "creator-tok" {
			t.Fatalf("%s token = %q, want creator-tok (root token leaked!)", key, c.token)
		}
	}
	// The shared client's token must never be mutated by brokering.
	if got := vc.c.Token(); got != "ROOT-TOKEN-MUST-NOT-BE-USED" {
		t.Fatalf("shared client token mutated to %q — creator token leaked onto the shared client", got)
	}
}

// TestCreatorWriteKV proves the descriptor write goes to the shared ROOT-namespace
// KV under the brokered creator token — not the standing portal token, and not a
// child namespace. This is the write that 403'd when it fell back to the read-only
// portal WIF token.
func TestCreatorWriteKV(t *testing.T) {
	got := map[string]capture{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/jwt-nomad/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{"client_token": "creator-tok", "lease_duration": 300},
		})
	})
	// KVv2.Put issues a preflight to resolve the mount's kv version.
	mux.HandleFunc("/v1/sys/internal/ui/mounts/secret", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"path": "secret/", "type": "kv", "options": map[string]any{"version": "2"}},
		})
	})
	mux.HandleFunc("/v1/secret/data/projects/project-beta/portal-descriptor", func(w http.ResponseWriter, r *http.Request) {
		got["descriptor"] = capture{ns: r.Header.Get("X-Vault-Namespace"), token: r.Header.Get("X-Vault-Token")}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"version": 1}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	vc := NewVaultCreator(newClient(t, srv.URL), CreatorConfig{JWTPath: writeJWT(t, "the-jwt"), Role: "project-creator", AuthMount: "jwt-nomad"}, JWKSConfig{})
	if err := vc.WriteKV(context.Background(), "projects/project-beta/portal-descriptor", map[string]any{"descriptor": "{}"}); err != nil {
		t.Fatalf("WriteKV: %v", err)
	}
	if c := got["descriptor"]; c.ns != "" || c.token != "creator-tok" {
		t.Fatalf("descriptor write ns=%q token=%q, want ns=\"\" token=creator-tok", c.ns, c.token)
	}
	if got := vc.c.Token(); got != "ROOT-TOKEN-MUST-NOT-BE-USED" {
		t.Fatalf("shared client token mutated to %q", got)
	}
}

// TestCreatorUnconfiguredIsBadRequest proves an unconfigured creator fails closed
// with ErrBadRequest and makes zero network calls.
func TestCreatorUnconfiguredIsBadRequest(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	vc := NewVaultCreator(newClient(t, srv.URL), CreatorConfig{}, JWKSConfig{})
	if vc.Enabled() {
		t.Fatalf("Enabled() = true for empty config")
	}
	err := vc.CreateNamespace(context.Background(), "project-acme")
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
	if called {
		t.Fatalf("made a Vault call despite missing config")
	}
}
