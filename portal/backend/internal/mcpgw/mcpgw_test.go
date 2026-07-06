package mcpgw

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeGateway is a minimal in-memory ContextForge stand-in covering exactly the
// endpoints mcpgw drives. The scoped client token is the literal "scoped-<serverID>";
// any other bearer is treated as the admin JWT.
type fakeGateway struct {
	peers   map[string]string // id -> name
	tools   map[string]string // toolID -> peerID
	servers map[string]string // id -> name
	tokens  map[string]string // id -> name
	nextID  int

	lastTransport string // transport sent on the most recent POST /gateways
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{
		peers:   map[string]string{},
		tools:   map[string]string{},
		servers: map[string]string{},
		tokens:  map[string]string{},
	}
}

func (f *fakeGateway) id(prefix string) string {
	f.nextID++
	return prefix + "-" + string(rune('a'+f.nextID))
}

func (f *fakeGateway) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /gateways", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]string{}
		for id, name := range f.peers {
			out = append(out, map[string]string{"id": id, "name": name})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /gateways", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Name, URL, Transport string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.lastTransport = in.Transport
		id := f.id("peer")
		f.peers[id] = in.Name
		// one tool auto-discovered for the new peer
		f.tools[f.id("tool")] = id
		writeJSON(w, 201, map[string]string{"id": id})
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, r *http.Request) {
		// A scoped client token must not reach the admin tool list.
		if isScoped(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		out := []map[string]string{}
		for id, peer := range f.tools {
			out = append(out, map[string]string{"id": id, "gatewayId": peer})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]string{}
		for id, name := range f.servers {
			out = append(out, map[string]string{"id": id, "name": name})
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /servers", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Server struct{ Name string } `json:"server"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := f.id("vs")
		f.servers[id] = in.Server.Name
		writeJSON(w, 201, map[string]string{"id": id})
	})
	mux.HandleFunc("GET /servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := f.servers[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// scoped token may read only its own server
		if tok := bearer(r); strings.HasPrefix(tok, "scoped-") {
			if strings.TrimPrefix(tok, "scoped-") != id {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		writeJSON(w, 200, map[string]string{"id": id, "name": f.servers[id]})
	})
	mux.HandleFunc("DELETE /servers/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(f.servers, r.PathValue("id"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("DELETE /gateways/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(f.peers, r.PathValue("id"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /tokens", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Name  string `json:"name"`
			Scope struct {
				ServerID string `json:"server_id"`
			} `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		id := f.id("tok")
		f.tokens[id] = in.Name
		writeJSON(w, 201, map[string]string{"access_token": "scoped-" + in.Scope.ServerID})
	})
	mux.HandleFunc("GET /tokens", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]string{}
		for id, name := range f.tokens {
			out = append(out, map[string]string{"id": id, "name": name})
		}
		writeJSON(w, 200, map[string]any{"tokens": out})
	})
	mux.HandleFunc("DELETE /tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		delete(f.tokens, r.PathValue("id"))
		w.WriteHeader(204)
	})
	return mux
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func isScoped(r *http.Request) bool { return strings.HasPrefix(bearer(r), "scoped-") }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func TestOnboardingFlow(t *testing.T) {
	gw := newFakeGateway()
	srv := httptest.NewServer(gw.handler())
	defer srv.Close()

	c := New(srv.URL, "admin@acme.example", "test-secret", srv.Client())
	ctx := context.Background()

	peerID, err := c.RegisterPeer(ctx, "vault-mcp", "http://node:9000/mcp", "streamable-http")
	if err != nil {
		t.Fatalf("RegisterPeer: %v", err)
	}
	// the portal transport is mapped onto ContextForge's enum
	if gw.lastTransport != "STREAMABLEHTTP" {
		t.Fatalf("transport sent to gateway = %q, want STREAMABLEHTTP", gw.lastTransport)
	}
	// idempotent: a second call reuses the same peer
	if again, err := c.RegisterPeer(ctx, "vault-mcp", "http://node:9000/mcp", "streamable-http"); err != nil || again != peerID {
		t.Fatalf("RegisterPeer idempotency: id=%s again=%s err=%v", peerID, again, err)
	}

	toolIDs, err := c.DiscoverTools(ctx, peerID)
	if err != nil || len(toolIDs) == 0 {
		t.Fatalf("DiscoverTools: ids=%v err=%v", toolIDs, err)
	}

	vsID, err := c.CreateVirtualServer(ctx, "vault-mcp-test", "test", toolIDs)
	if err != nil || vsID == "" {
		t.Fatalf("CreateVirtualServer: id=%s err=%v", vsID, err)
	}

	token, err := c.CreateScopedToken(ctx, "vault-mcp-test-abcd", 1, vsID)
	if err != nil || token == "" {
		t.Fatalf("CreateScopedToken: token=%q err=%v", token, err)
	}

	// Consumption-mirror probe: only one server exists, so isolation-vs-other is
	// skipped but own-server-200 and admin-403 must hold.
	probe, err := c.ProbeScopedToken(ctx, token, vsID, "")
	if err != nil {
		t.Fatalf("ProbeScopedToken: %v", err)
	}
	if !probe.OwnServerOK || !probe.AdminDenied || !probe.Passed() {
		t.Fatalf("probe failed: %+v", probe)
	}

	// Add a second server and confirm the first token is denied on it.
	otherID, _ := c.CreateVirtualServer(ctx, "other-srv", "other", toolIDs)
	probe2, err := c.ProbeScopedToken(ctx, token, vsID, otherID)
	if err != nil {
		t.Fatalf("ProbeScopedToken (2 servers): %v", err)
	}
	if !probe2.OtherServerChecked || !probe2.OtherServerDenied || !probe2.Passed() {
		t.Fatalf("isolation probe failed: %+v", probe2)
	}

	if err := c.RevokeTokensByPrefix(ctx, "vault-mcp-test-"); err != nil {
		t.Fatalf("RevokeTokensByPrefix: %v", err)
	}
	if err := c.DeleteVirtualServer(ctx, vsID); err != nil {
		t.Fatalf("DeleteVirtualServer: %v", err)
	}
	if err := c.DeletePeer(ctx, peerID); err != nil {
		t.Fatalf("DeletePeer: %v", err)
	}
}

// TestRegisterPeer_RetriesWhileContainerBoots pins the deploy-time race: the
// portal registers the peer right after Nomad places the alloc, and ContextForge
// validates the peer synchronously — a container not yet listening returns 502
// "Failed to initialize gateway" (observed live). RegisterPeer must retry that
// failure through the boot window, and fail fast on anything else.
func TestRegisterPeer_RetriesWhileContainerBoots(t *testing.T) {
	oldInterval := registerInterval
	registerInterval = 5 * time.Millisecond
	defer func() { registerInterval = oldInterval }()

	gw := newFakeGateway()
	failures := 3
	posts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /gateways", func(w http.ResponseWriter, r *http.Request) {
		posts++
		if posts <= failures {
			writeJSON(w, 502, map[string]string{"message": "Failed to initialize gateway at http://node:9000/mcp: All connection attempts failed"})
			return
		}
		gw.handler().ServeHTTP(w, r)
	})
	mux.Handle("/", gw.handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "admin@acme.example", "test-secret", srv.Client())
	id, err := c.RegisterPeer(context.Background(), "boot-mcp", "http://node:9000/mcp", "sse")
	if err != nil || id == "" {
		t.Fatalf("RegisterPeer must survive the boot window: id=%q err=%v", id, err)
	}
	if posts != failures+1 {
		t.Fatalf("attempts = %d, want %d", posts, failures+1)
	}

	// A non-init failure (e.g. a 400) must not retry.
	posts = 0
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /gateways", func(w http.ResponseWriter, r *http.Request) {
		posts++
		writeJSON(w, 400, map[string]string{"message": "Invalid request"})
	})
	mux2.Handle("/", newFakeGateway().handler())
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	c2 := New(srv2.URL, "admin@acme.example", "test-secret", srv2.Client())
	if _, err := c2.RegisterPeer(context.Background(), "bad-mcp", "http://node:9000/mcp", "sse"); err == nil {
		t.Fatal("400 must fail")
	}
	if posts != 1 {
		t.Fatalf("400 must not retry: attempts = %d", posts)
	}
}

// TestActivatePeer_StateWithToggleFallback pins the reactivation contract:
// ContextForge deactivates a peer after 3 failed health checks (observed live in
// the window between an MCP job restart and the peer heal), and reactivation is
// POST /gateways/{id}/state?activate=true — with the deprecated /toggle alias as
// the fallback for gateway builds that predate /state (404 there, nowhere else).
func TestActivatePeer_StateWithToggleFallback(t *testing.T) {
	var statePath, togglePath string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /gateways/peer-1/state", func(w http.ResponseWriter, r *http.Request) {
		statePath = r.URL.RawQuery
		writeJSON(w, 200, map[string]string{"status": "success"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL, "admin@acme.example", "test-secret", srv.Client())
	if err := c.ActivatePeer(context.Background(), "peer-1"); err != nil {
		t.Fatalf("ActivatePeer: %v", err)
	}
	if statePath != "activate=true" {
		t.Errorf("state query = %q, want activate=true", statePath)
	}

	// Older gateway: /state is 404 — must fall back to the deprecated /toggle.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /gateways/peer-1/toggle", func(w http.ResponseWriter, r *http.Request) {
		togglePath = r.URL.RawQuery
		writeJSON(w, 200, map[string]string{"status": "success"})
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()
	c2 := New(srv2.URL, "admin@acme.example", "test-secret", srv2.Client())
	if err := c2.ActivatePeer(context.Background(), "peer-1"); err != nil {
		t.Fatalf("ActivatePeer via /toggle fallback: %v", err)
	}
	if togglePath != "activate=true" {
		t.Errorf("toggle query = %q, want activate=true", togglePath)
	}
}

// TestCreateVirtualServer_ReplacesExistingByName pins the re-wire contract: after
// a peer delete+redeploy the old virtual server of the same name references dead
// tool ids (verified live), so CreateVirtualServer must REPLACE it — delete the
// stale one and compose a fresh VS from the new tool set — not reuse it.
func TestCreateVirtualServer_ReplacesExistingByName(t *testing.T) {
	gw := newFakeGateway()
	srv := httptest.NewServer(gw.handler())
	defer srv.Close()

	c := New(srv.URL, "admin@acme.example", "test-secret", srv.Client())
	ctx := context.Background()

	id1, err := c.CreateVirtualServer(ctx, "mcp-acme-vault", "old wiring", []string{"tool-old"})
	if err != nil {
		t.Fatalf("CreateVirtualServer (first): %v", err)
	}
	id2, err := c.CreateVirtualServer(ctx, "mcp-acme-vault", "re-wire", []string{"tool-new"})
	if err != nil {
		t.Fatalf("CreateVirtualServer (re-wire): %v", err)
	}
	if id2 == id1 {
		t.Fatalf("re-wire reused stale virtual server %s; want a fresh one", id1)
	}
	if _, stale := gw.servers[id1]; stale {
		t.Fatalf("stale virtual server %s not deleted", id1)
	}
	count := 0
	for _, n := range gw.servers {
		if n == "mcp-acme-vault" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d virtual servers named mcp-acme-vault, want exactly 1", count)
	}
}

func TestMintAdminJWT(t *testing.T) {
	c := &httpClient{adminEmail: "admin@acme.example", jwtSecret: "s3cr3t"}
	tok, err := c.mintAdminJWT()
	if err != nil {
		t.Fatalf("mintAdminJWT: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	// signature must verify against the secret
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != want {
		t.Fatalf("signature mismatch")
	}
	// claims carry the gateway-required iss/aud and the admin identity
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	if claims["iss"] != jwtIssuer || claims["aud"] != jwtAudience || claims["sub"] != "admin@acme.example" {
		t.Fatalf("unexpected claims: %v", claims)
	}
}
