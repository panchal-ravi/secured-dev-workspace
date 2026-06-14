// Package mcpgw is the portal's admin client for the shared ContextForge MCP
// gateway. It is a Go port of terraform/project/scripts/mcp-provision.sh: it
// mints the HS256 admin JWT the gateway accepts, registers a peer MCP server,
// waits for its tools to be discovered, composes a virtual server scoped to those
// tools, and mints a server-scoped client token — the exact path a Project Admin
// uses to consume an onboarded server. It is used both to register published
// servers and to run the consumption-mirror verification of a freshly-deployed one.
//
// Auth is JWT-only (Basic is disabled on the gateway). Admin calls carry an HS256
// JWT over the gateway's jwt_secret_key for the bootstrap admin identity; client
// (scoped) tokens are real DB-backed API tokens minted via POST /tokens, because
// hand-minted JWTs only authenticate the bootstrap admin (any other sub is 401).
package mcpgw

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the gateway admin surface the portal's onboarding flows depend on.
// Implemented by *httpClient; defined as an interface so the admin service can be
// tested against a fake gateway.
type Client interface {
	// RegisterPeer registers (or reuses, by name) a peer MCP server at url and
	// returns its gateway id.
	RegisterPeer(ctx context.Context, name, url string) (peerID string, err error)
	// DiscoverTools polls until the gateway has discovered the peer's tools,
	// returning their ids. Errors if none appear within the discovery window.
	DiscoverTools(ctx context.Context, peerID string) (toolIDs []string, err error)
	// CreateVirtualServer composes (or reuses, by name) a virtual server bound to
	// the given tool ids and returns its id.
	CreateVirtualServer(ctx context.Context, name, description string, toolIDs []string) (serverID string, err error)
	// CreateScopedToken mints a DB-backed client token scoped to serverID.
	CreateScopedToken(ctx context.Context, name string, expiresInDays int, serverID string) (token string, err error)
	// RevokeTokensByPrefix soft-deletes every active client token whose name has
	// the prefix (best-effort cleanup; ContextForge reserves deleted names).
	RevokeTokensByPrefix(ctx context.Context, prefix string) error
	// ProbeScopedToken verifies a server-scoped token behaves as a Project Admin
	// would experience it: 200 on its own server, 403 on admin and other servers.
	ProbeScopedToken(ctx context.Context, token, serverID, otherServerID string) (ScopeProbe, error)
	// DeleteVirtualServer removes a virtual server (best-effort teardown).
	DeleteVirtualServer(ctx context.Context, serverID string) error
	// DeletePeer removes a peer registration (best-effort teardown).
	DeletePeer(ctx context.Context, peerID string) error
}

// ScopeProbe is the outcome of the consumption-mirror scope check. Passed reports
// whether the token reached only its own virtual server.
type ScopeProbe struct {
	OwnServerOK        bool `json:"own_server_ok"`        // GET /servers/<own> == 200
	AdminDenied        bool `json:"admin_denied"`         // GET /tools (admin) == 403
	OtherServerDenied  bool `json:"other_server_denied"`  // GET /servers/<other> == 403 (only if an other server exists)
	OtherServerChecked bool `json:"other_server_checked"` // whether a second server was available to test isolation
}

// Passed reports whether the scoped token is correctly isolated to its server.
func (p ScopeProbe) Passed() bool {
	if !p.OwnServerOK || !p.AdminDenied {
		return false
	}
	if p.OtherServerChecked && !p.OtherServerDenied {
		return false
	}
	return true
}

const (
	// The gateway's defaults — the infra job does not override JWT_ISSUER /
	// JWT_AUDIENCE, so these must match what the gateway verifies (confirmed live).
	jwtIssuer   = "mcpgateway"
	jwtAudience = "mcpgateway-api"

	discoveryAttempts = 30
	discoveryInterval = 2 * time.Second
	adminTokenTTL     = 60 * time.Minute
)

// httpClient is the concrete ContextForge admin client.
type httpClient struct {
	baseURL    string
	adminEmail string
	jwtSecret  string
	hc         *http.Client
}

// New builds a gateway client. baseURL is the gateway admin URL (e.g.
// http://<node>:4444); adminEmail is the bootstrap admin identity the gateway
// accepts as a bare JWT; jwtSecret is the gateway's jwt_secret_key (from Vault).
func New(baseURL, adminEmail, jwtSecret string, hc *http.Client) Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &httpClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		adminEmail: adminEmail,
		jwtSecret:  jwtSecret,
		hc:         hc,
	}
}

func (c *httpClient) RegisterPeer(ctx context.Context, name, url string) (string, error) {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return "", err
	}
	// Idempotent: reuse an existing peer of the same name.
	if id, err := c.findByName(ctx, jwt, "/gateways", name); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	body, _ := json.Marshal(map[string]string{"name": name, "url": url})
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, jwt, http.MethodPost, "/gateways", body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", fmt.Errorf("mcpgw: gateway returned no peer id for %q", name)
	}
	return created.ID, nil
}

func (c *httpClient) DiscoverTools(ctx context.Context, peerID string) ([]string, error) {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return nil, err
	}
	for range discoveryAttempts {
		var tools []struct {
			ID         string `json:"id"`
			GatewayID  string `json:"gatewayId"`
			GatewayID2 string `json:"gateway_id"`
		}
		if err := c.call(ctx, jwt, http.MethodGet, "/tools", nil, &tools); err != nil {
			return nil, err
		}
		ids := []string{}
		for _, t := range tools {
			if t.GatewayID == peerID || t.GatewayID2 == peerID {
				ids = append(ids, t.ID)
			}
		}
		if len(ids) > 0 {
			return ids, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(discoveryInterval):
		}
	}
	return nil, fmt.Errorf("mcpgw: no tools discovered for peer %q after %ds", peerID, discoveryAttempts*int(discoveryInterval/time.Second))
}

func (c *httpClient) CreateVirtualServer(ctx context.Context, name, description string, toolIDs []string) (string, error) {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return "", err
	}
	if id, err := c.findByName(ctx, jwt, "/servers", name); err != nil {
		return "", err
	} else if id != "" {
		return id, nil
	}
	body, _ := json.Marshal(map[string]any{
		"server": map[string]any{
			"name":             name,
			"description":      description,
			"associated_tools": toolIDs,
		},
	})
	// POST /servers returns either {id} or {server:{id}} across gateway versions.
	var created struct {
		ID     string `json:"id"`
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	if err := c.call(ctx, jwt, http.MethodPost, "/servers", body, &created); err != nil {
		return "", err
	}
	id := created.ID
	if id == "" {
		id = created.Server.ID
	}
	if id == "" {
		return "", fmt.Errorf("mcpgw: gateway returned no virtual server id for %q", name)
	}
	return id, nil
}

func (c *httpClient) CreateScopedToken(ctx context.Context, name string, expiresInDays int, serverID string) (string, error) {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{
		"name":            name,
		"expires_in_days": expiresInDays,
		"scope":           map[string]string{"server_id": serverID},
	})
	var created struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.call(ctx, jwt, http.MethodPost, "/tokens", body, &created); err != nil {
		return "", err
	}
	if created.AccessToken == "" {
		return "", fmt.Errorf("mcpgw: gateway returned no access token for %q", name)
	}
	return created.AccessToken, nil
}

func (c *httpClient) RevokeTokensByPrefix(ctx context.Context, prefix string) error {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return err
	}
	var listed struct {
		Tokens []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tokens"`
	}
	if err := c.call(ctx, jwt, http.MethodGet, "/tokens?limit=100", nil, &listed); err != nil {
		return err
	}
	for _, t := range listed.Tokens {
		if strings.HasPrefix(t.Name, prefix) {
			// Best-effort: a token already inactive 409s; ignore individual errors.
			_ = c.call(ctx, jwt, http.MethodDelete, "/tokens/"+t.ID, nil, nil)
		}
	}
	return nil
}

func (c *httpClient) ProbeScopedToken(ctx context.Context, token, serverID, otherServerID string) (ScopeProbe, error) {
	var p ScopeProbe

	own, err := c.status(ctx, token, http.MethodGet, "/servers/"+serverID)
	if err != nil {
		return p, err
	}
	p.OwnServerOK = own == http.StatusOK

	admin, err := c.status(ctx, token, http.MethodGet, "/tools")
	if err != nil {
		return p, err
	}
	p.AdminDenied = admin == http.StatusForbidden || admin == http.StatusUnauthorized

	if otherServerID != "" && otherServerID != serverID {
		p.OtherServerChecked = true
		other, err := c.status(ctx, token, http.MethodGet, "/servers/"+otherServerID)
		if err != nil {
			return p, err
		}
		p.OtherServerDenied = other == http.StatusForbidden || other == http.StatusUnauthorized
	}
	return p, nil
}

func (c *httpClient) DeleteVirtualServer(ctx context.Context, serverID string) error {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return err
	}
	return c.call(ctx, jwt, http.MethodDelete, "/servers/"+serverID, nil, nil)
}

func (c *httpClient) DeletePeer(ctx context.Context, peerID string) error {
	jwt, err := c.mintAdminJWT()
	if err != nil {
		return err
	}
	return c.call(ctx, jwt, http.MethodDelete, "/gateways/"+peerID, nil, nil)
}

// findByName returns the id of the first element of a list endpoint whose .name
// equals name, or "" if none. Used for idempotent peer/server creation.
func (c *httpClient) findByName(ctx context.Context, jwt, path, name string) (string, error) {
	var items []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.call(ctx, jwt, http.MethodGet, path, nil, &items); err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Name == name {
			return it.ID, nil
		}
	}
	return "", nil
}

// call performs an authenticated gateway request and decodes a JSON response into
// out (nil out discards the body). A non-2xx status is an error.
func (c *httpClient) call(ctx context.Context, token, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("mcpgw: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("mcpgw: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("mcpgw: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("mcpgw: decode %s %s response: %w", method, path, err)
	}
	return nil
}

// status performs a request with an arbitrary bearer token and returns the HTTP
// status code, ignoring the body. Used by the scoped-token probe, where a 403/401
// is an expected, non-error outcome.
func (c *httpClient) status(ctx context.Context, token, method, path string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return 0, fmt.Errorf("mcpgw: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mcpgw: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// mintAdminJWT builds the HS256 JWT the gateway accepts for the bootstrap admin,
// with exactly the claims ContextForge verifies (sub/username/iss/aud/iat/exp/jti).
func (c *httpClient) mintAdminJWT() (string, error) {
	now := time.Now()
	jti, err := randHex(16)
	if err != nil {
		return "", err
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	claims := map[string]any{
		"sub":      c.adminEmail,
		"username": c.adminEmail,
		"iss":      jwtIssuer,
		"aud":      jwtAudience,
		"iat":      now.Unix(),
		"exp":      now.Add(adminTokenTTL).Unix(),
		"jti":      jti,
	}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signing := b64url(hb) + "." + b64url(cb)
	mac := hmac.New(sha256.New, []byte(c.jwtSecret))
	mac.Write([]byte(signing))
	sig := b64urlRaw(mac.Sum(nil))
	return signing + "." + sig, nil
}

func b64url(b []byte) string    { return base64.RawURLEncoding.EncodeToString(b) }
func b64urlRaw(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("mcpgw: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
