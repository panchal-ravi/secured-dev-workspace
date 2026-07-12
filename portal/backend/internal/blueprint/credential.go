package blueprint

import (
	"fmt"
	"strings"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
	"github.com/secured-dev-workspace/developer-portal/internal/jobrender"
)

// Credential sources. A CredentialSpec is the project-admin-authored recipe folded
// into the MCP deploy wizard (it replaces the platform-tier 3-class blueprint).
const (
	SourceNone     = "none"      // no Vault credential at all: no policy, no WIF role, no vault block
	SourceWIFToken = "wif-token" // the Nomad-WIF-minted VAULT_TOKEN is itself the credential
	SourceStatic   = "static"    // write-only static secret seeded into the project KV
	SourceDynamic  = "dynamic"   // a dynamic secrets engine (database, aws, ...) mints short-lived creds
)

// CredentialSpec is persisted on the project MCP-server row WITH ${param}
// placeholders intact; secret param VALUES are used once at Instantiate and are
// never stored or logged.
type CredentialSpec struct {
	Source       string            `json:"source"`
	TokenTTL     string            `json:"token_ttl,omitempty"` // WIF token TTL, default "1h"
	Params       []ParamSpec       `json:"params,omitempty"`
	Static       *StaticSpec       `json:"static,omitempty"`
	Dynamic      *DynamicSpec      `json:"dynamic,omitempty"`
	EnvTemplates map[string]string `json:"env_templates,omitempty"` // two-layer render: ${...} now, {{ }} at runtime
}

// StaticSpec seeds one write-only KV secret. Values may carry ${param} tokens
// (typically a single secret param), interpolated once at deploy.
type StaticSpec struct {
	Data map[string]string `json:"data"`
}

// DynamicSpec is a generic dynamic-engine recipe: mount the engine, apply the
// config/role writes (relative to the mount), and grant read on the creds path.
// Engine-specific shapes (PostgreSQL, AWS, ...) live entirely in the write
// payloads, so a new engine is a new UI preset — no code change.
type DynamicSpec struct {
	Engine         string         `json:"engine"`                     // Vault engine type: "database", "aws", ...
	Mount          string         `json:"mount"`                      // e.g. "database/postgres-mcp"
	Configs        []LogicalWrite `json:"configs,omitempty"`          // e.g. config/conn
	RotateRootPath string         `json:"rotate_root_path,omitempty"` // e.g. "rotate-root/conn" (database), "config/rotate-root" (aws)
	Role           *LogicalWrite  `json:"role,omitempty"`             // e.g. roles/mcp-ro
	CredsPath      string         `json:"creds_path"`                 // relative to Mount, e.g. "creds/mcp-ro"
	CredsCaps      []string       `json:"creds_caps,omitempty"`       // policy capabilities on the creds path; default ["read"]
	LeaseBased     *bool          `json:"lease_based,omitempty"`      // default true: the creds path is a lease prefix to revoke at teardown
}

// LogicalWrite is one Vault logical write relative to the engine mount. String
// leaves in Data (including inside nested maps/lists) may carry ${param} tokens.
type LogicalWrite struct {
	Path string         `json:"path"`
	Data map[string]any `json:"data,omitempty"`
}

var validSource = map[string]bool{SourceNone: true, SourceWIFToken: true, SourceStatic: true, SourceDynamic: true}

// Validate checks the structural invariants of the spec (not parameter values).
func (c CredentialSpec) Validate() error {
	if !validSource[c.Source] {
		return fmt.Errorf("credential: source must be none, wif-token, static or dynamic: %w", apperr.ErrBadRequest)
	}
	for _, p := range c.Params {
		if p.Name == "" {
			return fmt.Errorf("credential: param name required: %w", apperr.ErrBadRequest)
		}
		if p.Type != "string" && p.Type != "int" && p.Type != "secret" {
			return fmt.Errorf("credential: param %q type must be string, int or secret: %w", p.Name, apperr.ErrBadRequest)
		}
	}
	switch c.Source {
	case SourceStatic:
		if c.Dynamic != nil {
			return fmt.Errorf("credential: static source must not carry a dynamic spec: %w", apperr.ErrBadRequest)
		}
		if c.Static == nil || len(c.Static.Data) == 0 {
			return fmt.Errorf("credential: static source needs data fields: %w", apperr.ErrBadRequest)
		}
	case SourceDynamic:
		if c.Static != nil {
			return fmt.Errorf("credential: dynamic source must not carry a static spec: %w", apperr.ErrBadRequest)
		}
		d := c.Dynamic
		if d == nil || d.Engine == "" || d.Mount == "" || d.CredsPath == "" {
			return fmt.Errorf("credential: dynamic source needs engine, mount and creds_path: %w", apperr.ErrBadRequest)
		}
		if err := lintWritePath(strings.TrimSuffix(d.Mount, "/")); err != nil {
			return err
		}
		if err := lintWritePath(d.CredsPath); err != nil {
			return err
		}
		for _, w := range d.Configs {
			if err := lintWritePath(w.Path); err != nil {
				return err
			}
		}
		if d.RotateRootPath != "" {
			if err := lintWritePath(d.RotateRootPath); err != nil {
				return err
			}
		}
		if d.Role != nil {
			if err := lintWritePath(d.Role.Path); err != nil {
				return err
			}
		}
		for _, cap := range d.CredsCaps {
			if !safeGrantCaps[cap] {
				return fmt.Errorf("credential: creds capability %q not allowed: %w", cap, apperr.ErrForbidden)
			}
		}
	default: // none, wif-token
		if c.Static != nil || c.Dynamic != nil {
			return fmt.Errorf("credential: source %q carries no static/dynamic spec: %w", c.Source, apperr.ErrBadRequest)
		}
	}
	return nil
}

// NonSecretParams returns a copy of params with every secret-typed value removed —
// the only shape safe to persist or log.
func (c CredentialSpec) NonSecretParams(params map[string]string) map[string]string {
	secret := map[string]bool{}
	for _, p := range c.Params {
		if p.Type == "secret" {
			secret[p.Name] = true
		}
	}
	out := make(map[string]string, len(params))
	for k, v := range params {
		if !secret[k] {
			out[k] = v
		}
	}
	return out
}

// tokenTTL returns the WIF token TTL with its default applied.
func (c CredentialSpec) tokenTTL() string {
	if c.TokenTTL == "" {
		return "1h"
	}
	return c.TokenTTL
}

// leaseBased reports whether the dynamic creds path mints revocable leases
// (default true; e.g. false would suit an engine whose creds are not leased).
func (d DynamicSpec) leaseBased() bool {
	return d.LeaseBased == nil || *d.LeaseBased
}

// credsCaps returns the policy capabilities for the creds path, defaulting to read.
func (d DynamicSpec) credsCaps() []string {
	if len(d.CredsCaps) == 0 {
		return []string{"read"}
	}
	return d.CredsCaps
}

// lintWritePath rejects write-path shapes that could escape the engine mount the
// executor prefixes every write with: empty, absolute, or traversal segments.
func lintWritePath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("credential: write path is empty: %w", apperr.ErrBadRequest)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("credential: write path %q must be relative to the mount: %w", p, apperr.ErrBadRequest)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("credential: write path %q contains a traversal segment: %w", p, apperr.ErrForbidden)
	}
	return nil
}

// interpolateValue substitutes ${param} tokens in every string leaf of v
// (recursing into maps and lists), erroring on any unresolved placeholder so a
// typo cannot silently write a literal "${...}" into Vault.
func interpolateValue(v any, params map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		return jobrender.Render(t, params)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			r, err := interpolateValue(e, params)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []string:
		out := make([]any, len(t))
		for i, e := range t {
			r, err := jobrender.Render(e, params)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			r, err := interpolateValue(e, params)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

// interpolateMap applies interpolateValue over a write payload.
func interpolateMap(data map[string]any, params map[string]string) (map[string]any, error) {
	out, err := interpolateValue(data, params)
	if err != nil {
		return nil, fmt.Errorf("credential: %w: %v", apperr.ErrBadRequest, err)
	}
	if out == nil {
		return nil, nil
	}
	return out.(map[string]any), nil
}

// DeriveCredentialPolicy builds the least-privilege policy for the spec: exactly
// the credential read path, nothing user-authored. The caller still runs the
// result through LintPolicy (defense in depth).
func DeriveCredentialPolicy(spec CredentialSpec, kvMount, serverName string) string {
	var path string
	caps := []string{"read"}
	switch spec.Source {
	case SourceDynamic:
		path = spec.Dynamic.Mount + "/" + spec.Dynamic.CredsPath
		caps = spec.Dynamic.credsCaps()
	case SourceStatic:
		path = kvMount + "/data/projects/mcp-secrets/" + serverName
	case SourceWIFToken:
		path = kvMount + "/data/projects/*"
	default:
		return ""
	}
	quoted := make([]string, len(caps))
	for i, c := range caps {
		quoted[i] = fmt.Sprintf("%q", c)
	}
	policy := fmt.Sprintf("path %q {\n  capabilities = [%s]\n}\n", path, strings.Join(quoted, ", "))
	if spec.Source == SourceWIFToken {
		// wif-token means the token IS the credential — the server talks to Vault
		// directly, and the official vault-mcp-server probes sys/mounts (namespace-
		// relative, read-only) to detect KV versions before any read. Baseline it so
		// every wif-token deploy works without a hand-added grant.
		policy += fmt.Sprintf("path %q {\n  capabilities = [\"read\"]\n}\n", sysMountsPath)
	}
	return policy
}
