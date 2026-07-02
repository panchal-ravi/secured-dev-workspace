package blueprint

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

// writeJWT drops a fake workload-identity JWT into a temp file and returns its path.
func writeJWT(t *testing.T, contents string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nomad_vault_provisioner.jwt")
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

// TestBrokersNamespaceNativeToken proves the provisioner logs into the child
// namespace and that subsequent writes carry the BROKERED token + namespace, not
// the portal's root token. It also proves the token is cached across sub-calls.
func TestBrokersNamespaceNativeToken(t *testing.T) {
	var loginCalls int
	var loginNS, loginRole, loginJWT string
	var writeToken, writeNS string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/jwt-nomad/login", func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		loginNS = r.Header.Get("X-Vault-Namespace")
		var body struct{ Role, JWT string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		loginRole, loginJWT = body.Role, body.JWT
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{"client_token": "brokered-tok", "lease_duration": 300},
		})
	})
	// PutPolicy → PUT /v1/sys/policies/acl/<name>
	mux.HandleFunc("/v1/sys/policies/acl/p-acme", func(w http.ResponseWriter, r *http.Request) {
		writeToken = r.Header.Get("X-Vault-Token")
		writeNS = r.Header.Get("X-Vault-Namespace")
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	va := NewVaultAdmin(newClient(t, srv.URL), ProvisionerConfig{
		JWTPath: writeJWT(t, "the-jwt"), Role: "portal-provisioner", AuthMount: "jwt-nomad",
	})
	ctx := context.Background()

	if err := va.WritePolicy(ctx, "project-acme", "p-acme", `path "x" {}`); err != nil {
		t.Fatalf("WritePolicy: %v", err)
	}
	// A second op in the same namespace must reuse the cached token (no re-login).
	if err := va.WritePolicy(ctx, "project-acme", "p-acme", `path "y" {}`); err != nil {
		t.Fatalf("WritePolicy #2: %v", err)
	}

	if loginCalls != 1 {
		t.Fatalf("login calls = %d, want 1 (token should be cached)", loginCalls)
	}
	if loginNS != "project-acme" || loginRole != "portal-provisioner" || loginJWT != "the-jwt" {
		t.Fatalf("login ns=%q role=%q jwt=%q", loginNS, loginRole, loginJWT)
	}
	if writeToken != "brokered-tok" {
		t.Fatalf("write used token %q, want brokered-tok (root token leaked!)", writeToken)
	}
	if writeNS != "project-acme" {
		t.Fatalf("write namespace = %q, want project-acme", writeNS)
	}
	// The security property: brokering must never mutate the shared root client's
	// token. Guards against a regression in the vault SDK's clone semantics.
	if got := va.(*vaultAdmin).c.Token(); got != "ROOT-TOKEN-MUST-NOT-BE-USED" {
		t.Fatalf("root client token mutated to %q — provisioner token leaked onto the shared client", got)
	}
}

// TestEngineWritesUseBrokeredToken proves the SSH/GitHub/periodic-token methods all
// carry the brokered namespace-native token (never the root token) and post the
// expected payloads to the expected namespace-local paths.
func TestEngineWritesUseBrokeredToken(t *testing.T) {
	seen := map[string]map[string]any{} // path -> decoded JSON body
	tokens := map[string]string{}       // path -> X-Vault-Token
	nss := map[string]string{}          // path -> X-Vault-Namespace

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/auth/jwt-nomad/login", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"auth": map[string]any{"client_token": "brokered-tok", "lease_duration": 300},
		})
	})
	record := func(path string, resp map[string]any) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			seen[path] = body
			tokens[path] = r.Header.Get("X-Vault-Token")
			nss[path] = r.Header.Get("X-Vault-Namespace")
			if resp != nil {
				_ = json.NewEncoder(w).Encode(resp)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		})
	}
	record("/v1/ssh/config/ca", nil)
	record("/v1/ssh/roles/dev-workspace", nil)
	record("/v1/github/config", nil)
	record("/v1/github/permissionset/dev-workspace", nil)
	record("/v1/auth/token/roles/periodic-boundary-cred-store", nil)
	record("/v1/auth/token/create/periodic-boundary-cred-store", map[string]any{
		"auth": map[string]any{"client_token": "periodic-tok"},
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	va := NewVaultAdmin(newClient(t, srv.URL), ProvisionerConfig{
		JWTPath: writeJWT(t, "the-jwt"), Role: "portal-provisioner", AuthMount: "jwt-nomad",
	})
	ctx := context.Background()
	ns := "project-beta"

	if err := va.WriteSSHCA(ctx, ns, "ssh"); err != nil {
		t.Fatalf("WriteSSHCA: %v", err)
	}
	if err := va.WriteSSHRole(ctx, ns, "ssh", "dev-workspace", SSHRole{AllowedUsers: "dev", DefaultUser: "dev", TTL: "5m", MaxTTL: "30m"}); err != nil {
		t.Fatalf("WriteSSHRole: %v", err)
	}
	if err := va.WriteGitHubConfig(ctx, ns, "github", 42, "PEM"); err != nil {
		t.Fatalf("WriteGitHubConfig: %v", err)
	}
	if err := va.WriteGitHubPermissionSet(ctx, ns, "github", "dev-workspace", 99, map[string]string{"contents": "write"}, []string{"acme/repo"}); err != nil {
		t.Fatalf("WriteGitHubPermissionSet: %v", err)
	}
	tok, err := va.CreatePeriodicToken(ctx, ns, []string{"boundary-cred-store"}, "24h")
	if err != nil {
		t.Fatalf("CreatePeriodicToken: %v", err)
	}
	if tok != "periodic-tok" {
		t.Fatalf("periodic token = %q, want periodic-tok", tok)
	}

	for path, tk := range tokens {
		if tk != "brokered-tok" {
			t.Fatalf("%s used token %q, want brokered-tok (root token leaked!)", path, tk)
		}
		if nss[path] != ns {
			t.Fatalf("%s namespace = %q, want %q", path, nss[path], ns)
		}
	}
	if seen["/v1/ssh/config/ca"]["generate_signing_key"] != true || seen["/v1/ssh/config/ca"]["key_type"] != "ed25519" {
		t.Fatalf("ssh ca payload = %v", seen["/v1/ssh/config/ca"])
	}
	if seen["/v1/github/config"]["prv_key"] != "PEM" {
		t.Fatalf("github config payload = %v", seen["/v1/github/config"])
	}
	role := seen["/v1/auth/token/roles/periodic-boundary-cred-store"]
	if role["orphan"] != true {
		t.Fatalf("token role payload = %v", role)
	}
	if pols, ok := role["allowed_policies"].([]any); !ok || len(pols) != 1 || pols[0] != "boundary-cred-store" {
		t.Fatalf("token role allowed_policies = %v", role["allowed_policies"])
	}
	if create := seen["/v1/auth/token/create/periodic-boundary-cred-store"]; create["period"] != "24h" {
		t.Fatalf("token create payload = %v", create)
	}
}

// TestUnconfiguredIsBadRequest proves that with no provisioner JWT path the admin
// fails closed with ErrBadRequest and makes ZERO network calls.
func TestUnconfiguredIsBadRequest(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	va := NewVaultAdmin(newClient(t, srv.URL), ProvisionerConfig{}) // empty
	err := va.WritePolicy(context.Background(), "project-acme", "p", `path "x" {}`)
	if !errors.Is(err, apperr.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
	if called {
		t.Fatalf("made a Vault call despite missing config")
	}
}
