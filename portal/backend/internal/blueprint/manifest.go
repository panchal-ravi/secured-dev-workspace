// Package blueprint is the platform-tier credential blueprint engine. A
// BlueprintManifest is a version-pinned, content-hashed recipe that, instantiated
// into a project's Vault namespace, provisions the secret engine(s), a generated
// least-privilege policy, and the WIF binding an MCP server needs. Platform
// engineering authors blueprints; a project supplies only declared parameters.
package blueprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/secured-dev-workspace/developer-portal/internal/apperr"
)

// Credential classes (spec §4 / design D5).
const (
	ClassA = "A" // dynamic broker: a database engine mints short-lived creds
	ClassB = "B" // static upstream secret: write-only KV seed
	ClassC = "C" // vault-token: the WIF token is itself the credential
)

// BlueprintManifest is the immutable, content-hashed artifact. A new revision is a
// new Version (a new immutable manifest), never an in-place edit.
type BlueprintManifest struct {
	ID          string       `json:"id"`
	Version     int          `json:"version"`
	Class       string       `json:"class"`
	Description string       `json:"description"`
	Engines     []EngineSpec `json:"engines,omitempty"`
	Role        *RoleSpec    `json:"role,omitempty"`
	PolicyTpl   string       `json:"policy_tpl"`
	WIFRole     WIFRoleSpec  `json:"wif_role"`
	Params      []ParamSpec  `json:"params,omitempty"`
}

// EngineSpec is a secret engine to mount. MountPathTpl may reference {{.Namespace}};
// Plugin is the backend plugin for a database engine (Class A).
type EngineSpec struct {
	Type         string `json:"type"` // "database" | "kv-v2"
	Plugin       string `json:"plugin,omitempty"`
	MountPathTpl string `json:"mount_path_tpl"`
}

// RoleSpec is a dynamic database role (Class A): creation statements + TTLs.
type RoleSpec struct {
	NameTpl            string   `json:"name_tpl"`
	CreationStatements []string `json:"creation_statements"`
	DefaultTTLSeconds  int      `json:"default_ttl_seconds"`
	MaxTTLSeconds      int      `json:"max_ttl_seconds"`
}

// ParamSpec is a declared input. Type "secret" values are write-only: seeded into
// Vault, never persisted to the control-plane store, never logged.
type ParamSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "string" | "int" | "secret"
	Required bool   `json:"required"`
	Prompt   string `json:"prompt,omitempty"`
}

// WIFRoleSpec is the Nomad-WIF role the MCP job binds. The role is always bound to
// the single least-privilege policy the executor generates (named from NameTpl), so
// the manifest declares no policy names of its own.
type WIFRoleSpec struct {
	NameTpl  string `json:"name_tpl"`
	TokenTTL string `json:"token_ttl"`
}

// BlueprintRef is the immutable pin a catalog entry / deployed instance records.
type BlueprintRef struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

// InstanceRecord is what Instantiate returns and Deprovision consumes — the exact
// teardown manifest, including the lease prefixes that must be revoked first.
type InstanceRecord struct {
	Ref           BlueprintRef `json:"ref"`
	Namespace     string       `json:"namespace"`
	Mounts        []string     `json:"mounts,omitempty"`
	PolicyNames   []string     `json:"policy_names,omitempty"`
	WIFRoleName   string       `json:"wif_role_name,omitempty"`
	LeasePrefixes []string     `json:"lease_prefixes,omitempty"`
}

var validClass = map[string]bool{ClassA: true, ClassB: true, ClassC: true}

// Validate checks structural/per-class invariants (not the live behaviour — that
// is the Validator). It does not check parameter values.
func (m BlueprintManifest) Validate() error {
	if m.ID == "" || m.Version < 1 {
		return fmt.Errorf("blueprint: id and version>=1 required: %w", apperr.ErrBadRequest)
	}
	if !validClass[m.Class] {
		return fmt.Errorf("blueprint: class must be A, B or C: %w", apperr.ErrBadRequest)
	}
	if m.PolicyTpl == "" {
		return fmt.Errorf("blueprint: policy_tpl required: %w", apperr.ErrBadRequest)
	}
	if m.WIFRole.NameTpl == "" {
		return fmt.Errorf("blueprint: wif_role name_tpl required: %w", apperr.ErrBadRequest)
	}
	switch m.Class {
	case ClassA:
		if len(m.Engines) != 1 || m.Engines[0].Type != "database" {
			return fmt.Errorf("blueprint: class A needs exactly one database engine: %w", apperr.ErrBadRequest)
		}
		if m.Role == nil || len(m.Role.CreationStatements) == 0 {
			return fmt.Errorf("blueprint: class A needs a role with creation_statements: %w", apperr.ErrBadRequest)
		}
		if !m.hasSecretParam() {
			return fmt.Errorf("blueprint: class A needs a write-only bootstrap secret param: %w", apperr.ErrBadRequest)
		}
	case ClassB:
		if !m.hasSecretParam() {
			return fmt.Errorf("blueprint: class B needs a write-only upstream secret param: %w", apperr.ErrBadRequest)
		}
	case ClassC:
		if len(m.Engines) != 0 || m.Role != nil {
			return fmt.Errorf("blueprint: class C has no engine or role: %w", apperr.ErrBadRequest)
		}
	}
	return nil
}

func (m BlueprintManifest) hasSecretParam() bool {
	for _, p := range m.Params {
		if p.Type == "secret" {
			return true
		}
	}
	return false
}

// ContentHash is the SHA-256 over a canonical (sorted-key, sorted-params) JSON
// serialization, so logically-equal manifests hash equal regardless of field order.
func (m BlueprintManifest) ContentHash() string {
	c := m
	c.Params = append([]ParamSpec(nil), m.Params...)
	sort.Slice(c.Params, func(i, j int) bool { return c.Params[i].Name < c.Params[j].Name })
	// json.Marshal sorts struct fields by declaration and map keys lexically, so a
	// fixed struct shape + pre-sorted slices yields a stable encoding.
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Ref returns the immutable pin for this manifest.
func (m BlueprintManifest) Ref() BlueprintRef {
	return BlueprintRef{ID: m.ID, Version: m.Version, ContentHash: m.ContentHash()}
}
