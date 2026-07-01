// Package projectbootstrap creates a project's Tier-1 shell — the privileged,
// multi-plane bootstrap (Vault child namespace + auth, Nomad namespace + ACL,
// Boundary scope, descriptor) that a portal-admin drives from the Portal instead
// of running the terraform/project tier by hand.
//
// VaultCreator is the Vault half: it brokers a short-TTL token from the ROOT
// namespace jwt-nomad backend (role project-creator) and uses it to create the
// child namespace and seed its auth so the §5 portal-provisioner broker can take
// over for in-namespace engine work. It never mutates the shared root client.
package projectbootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	vapi "github.com/hashicorp/vault/api"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// CreatorConfig points the creator broker at the Nomad workload-identity JWT and
// the ROOT-namespace jwt-nomad role it exchanges for a namespace-creating token.
// An empty JWTPath disables the broker (fails closed with ErrBadRequest).
type CreatorConfig struct {
	JWTPath   string // Nomad-written workload-identity JWT file (aud=vault-creator)
	Role      string // jwt-nomad role, e.g. "project-creator"
	AuthMount string // auth mount, e.g. "jwt-nomad"
}

// JWKSConfig is the Nomad JWKS trust the creator installs into each new project
// namespace's jwt-nomad backend so project workloads can federate.
type JWKSConfig struct {
	URL   string
	CAPEM string
}

// VaultCreator brokers a project-creator token and performs the create-time Vault
// writes. The token is minted in the ROOT namespace and reused (cached for its
// short TTL) across the handful of writes a single project create needs.
type VaultCreator struct {
	c    *vapi.Client
	cfg  CreatorConfig
	jwks JWKSConfig

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// NewVaultCreator wraps a configured Vault API client with the creator-broker config.
func NewVaultCreator(c *vapi.Client, cfg CreatorConfig, jwks JWKSConfig) *VaultCreator {
	return &VaultCreator{c: c, cfg: cfg, jwks: jwks}
}

// Enabled reports whether the project-create plane is configured.
func (v *VaultCreator) Enabled() bool { return v.cfg.JWTPath != "" }

// broker returns a valid creator token (root namespace), minting one via JWT login
// when the cache is empty or near expiry. The JWT is re-read from disk each login
// so identity rotation is picked up. Fails closed when unconfigured.
func (v *VaultCreator) broker(ctx context.Context) (string, error) {
	if v.cfg.JWTPath == "" {
		return "", fmt.Errorf("projectbootstrap: creator broker not configured (project-create plane disabled): %w", apperr.ErrBadRequest)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && time.Until(v.expiry) > 30*time.Second {
		return v.token, nil
	}
	jwt, err := os.ReadFile(v.cfg.JWTPath)
	if err != nil {
		return "", fmt.Errorf("projectbootstrap: read creator jwt: %w", err)
	}
	// Login in the ROOT namespace on an independent clone so the shared client's
	// token/namespace are never mutated.
	login := v.c.WithNamespace("")
	login.ClearToken()
	sec, err := login.Logical().WriteWithContext(ctx, "auth/"+v.cfg.AuthMount+"/login", map[string]any{
		"role": v.cfg.Role,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return "", wrap("creator login", err)
	}
	if sec == nil || sec.Auth == nil || sec.Auth.ClientToken == "" {
		return "", fmt.Errorf("projectbootstrap: creator login returned no token")
	}
	v.token = sec.Auth.ClientToken
	v.expiry = time.Now().Add(time.Duration(sec.Auth.LeaseDuration) * time.Second)
	return v.token, nil
}

// root returns a root-namespace client authenticated with the brokered token.
func (v *VaultCreator) root(ctx context.Context) (*vapi.Client, error) {
	tok, err := v.broker(ctx)
	if err != nil {
		return nil, err
	}
	cl := v.c.WithNamespace("") // independent clone, root namespace
	cl.SetToken(tok)
	return cl, nil
}

// child returns a client scoped to namespace ns, authenticated with the brokered
// token. The creator's policy grants the child-namespace-prefixed paths (there is
// no auth backend to log into yet in a freshly created namespace).
func (v *VaultCreator) child(ctx context.Context, ns string) (*vapi.Client, error) {
	tok, err := v.broker(ctx)
	if err != nil {
		return nil, err
	}
	cl := v.c.WithNamespace(ns) // independent clone
	cl.SetToken(tok)
	return cl, nil
}

// CreateNamespace creates the Vault Enterprise child namespace. Idempotent: an
// "already exists" response is treated as success.
func (v *VaultCreator) CreateNamespace(ctx context.Context, name string) error {
	cl, err := v.root(ctx)
	if err != nil {
		return err
	}
	if _, err := cl.Logical().WriteWithContext(ctx, "sys/namespaces/"+name, nil); err != nil {
		if isAlreadyExists(err) {
			return nil
		}
		return wrap("create namespace", err)
	}
	return nil
}

// EnableJWTNomad enables + configures the jwt-nomad auth backend inside the child
// namespace against the Nomad JWKS trust. Idempotent on the enable step.
func (v *VaultCreator) EnableJWTNomad(ctx context.Context, ns string) error {
	cl, err := v.child(ctx, ns)
	if err != nil {
		return err
	}
	if err := cl.Sys().EnableAuthWithOptionsWithContext(ctx, "jwt-nomad", &vapi.EnableAuthOptions{Type: "jwt"}); err != nil {
		if !isAlreadyInUse(err) {
			return wrap("enable jwt-nomad", err)
		}
	}
	_, err = cl.Logical().WriteWithContext(ctx, "auth/jwt-nomad/config", map[string]any{
		"jwks_url":    v.jwks.URL,
		"jwks_ca_pem": v.jwks.CAPEM,
	})
	return wrap("configure jwt-nomad", err)
}

// MountSecretKV mounts the kv-v2 "secret" engine inside the child namespace (the
// project's runtime-coordinates store, mirroring terraform/project vault_mount.kv).
// Idempotent on the already-mounted case.
func (v *VaultCreator) MountSecretKV(ctx context.Context, ns string) error {
	cl, err := v.child(ctx, ns)
	if err != nil {
		return err
	}
	err = cl.Sys().MountWithContext(ctx, "secret", &vapi.MountInput{
		Type:    "kv",
		Options: map[string]string{"version": "2"},
	})
	if err != nil && !isAlreadyInUse(err) {
		return wrap("mount secret kv", err)
	}
	return nil
}

// SeedProvisioner writes the §5 portal-provisioner policy + role into the child
// namespace so the portal's provisioner broker can provision engines there.
func (v *VaultCreator) SeedProvisioner(ctx context.Context, ns string) error {
	cl, err := v.child(ctx, ns)
	if err != nil {
		return err
	}
	if err := cl.Sys().PutPolicyWithContext(ctx, "portal-provisioner", provisionerPolicyHCL); err != nil {
		return wrap("write portal-provisioner policy", err)
	}
	_, err = cl.Logical().WriteWithContext(ctx, "auth/jwt-nomad/role/portal-provisioner", map[string]any{
		"role_type":               "jwt",
		"bound_audiences":         []string{"vault-provisioner"},
		"user_claim":              "/nomad_job_id",
		"user_claim_json_pointer": true,
		"bound_claims":            map[string]any{"nomad_namespace": "infra", "nomad_job_id": "developer-portal"},
		"token_policies":          []string{"portal-provisioner"},
		"token_ttl":               300,
		"token_type":              "service",
	})
	return wrap("write portal-provisioner role", err)
}

// SeedWorkspaceRole writes the per-project workspace WIF read role shell. Its
// token_policies name the Phase-3 engine policies; Vault does not require them to
// exist at role-create time (they must exist at token-issuance time).
func (v *VaultCreator) SeedWorkspaceRole(ctx context.Context, ns, project string) error {
	cl, err := v.child(ctx, ns)
	if err != nil {
		return err
	}
	_, err = cl.Logical().WriteWithContext(ctx, "auth/jwt-nomad/role/"+project, map[string]any{
		"role_type":               "jwt",
		"bound_audiences":         []string{"vault.io"},
		"user_claim":              "/nomad_job_id",
		"user_claim_json_pointer": true,
		"token_policies":          workspacePolicyNames(project),
		"token_ttl":               1800,
		"token_max_ttl":           3600,
		"token_type":              "service",
	})
	return wrap("write workspace wif role", err)
}

// workspacePolicyNames is the set of per-project read policies the workspace WIF
// role carries — created later by the Phase-3 engine templates.
func workspacePolicyNames(project string) []string {
	return []string{
		"nomad-" + project + "-ca-read",
		"nomad-" + project + "-github-token",
		"nomad-" + project + "-db-creds",
		"nomad-" + project + "-llm-read",
		"nomad-" + project + "-mcp-read",
	}
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

func isAlreadyInUse(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already in use")
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("projectbootstrap: %s: %w", op, err)
}

// provisionerPolicyHCL is the §5 namespace-local provisioning policy, kept in sync
// with terraform/project/portal-provisioner.tf. Relative paths (a namespace-native
// token evaluates them correctly); denies win.
const provisionerPolicyHCL = `path "sys/mounts" { capabilities = ["read"] }
path "sys/mounts/*" { capabilities = ["create", "read", "update", "delete"] }
path "sys/policies/acl" { capabilities = ["list"] }
path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "delete", "list"] }
path "auth/jwt-nomad/role/*" { capabilities = ["create", "read", "update", "delete"] }
path "database/*" { capabilities = ["create", "read", "update", "delete"] }
path "secret/*" { capabilities = ["create", "read", "update", "delete"] }
path "sys/leases/revoke-force/*" { capabilities = ["update"] }

path "identity/*" { capabilities = ["deny"] }
path "sys/auth" { capabilities = ["deny"] }
path "sys/auth/*" { capabilities = ["deny"] }
path "sys/namespaces" { capabilities = ["deny"] }
path "sys/namespaces/*" { capabilities = ["deny"] }
path "cubbyhole/*" { capabilities = ["deny"] }
path "sys/policies/acl/portal-provisioner" { capabilities = ["deny"] }
`
