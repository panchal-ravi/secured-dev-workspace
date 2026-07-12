// Package blueprint is the project-tier credential engine for MCP servers. A
// project-admin's deploy wizard supplies a CredentialSpec (credential.go); the
// Executor instantiates it into the project's Vault namespace — mounting engines,
// seeding secrets, deriving a least-privilege policy and the WIF binding the MCP
// job authenticates with — and returns the exact teardown record.
package blueprint

// PathGrant is a project-admin-supplied additional policy grant applied at deploy
// time on top of the derived least-privilege policy. It is confined to the
// project's Vault namespace (the WIF token's boundary) and linted against the
// control-plane deny-list, but — unlike the credential's own paths — is NOT held
// to a mount allowlist, since it deliberately names the project's other mounts.
type PathGrant struct {
	Path         string   `json:"path"`
	Capabilities []string `json:"capabilities"`
}

// ParamSpec is a declared input. Type "secret" values are write-only: seeded into
// Vault, never persisted to the control-plane store, never logged.
type ParamSpec struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "string" | "int" | "secret"
	Required bool   `json:"required"`
	Prompt   string `json:"prompt,omitempty"`
}

// BlueprintRef survives only for LEGACY persisted state: rows and InstanceRecords
// written by the retired platform-catalog plane pinned the blueprint they were
// deployed from. New records never populate it.
type BlueprintRef struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	ContentHash string `json:"content_hash"`
}

// InstanceRecord is what Instantiate returns and Deprovision consumes — the exact
// teardown manifest, including the lease prefixes that must be revoked first.
type InstanceRecord struct {
	// Ref is legacy (catalog-era records); wizard-era records leave it zero.
	Ref           BlueprintRef `json:"ref,omitempty"`
	Namespace     string       `json:"namespace"`
	Mounts        []string     `json:"mounts,omitempty"`
	PolicyNames   []string     `json:"policy_names,omitempty"`
	WIFRoleName   string       `json:"wif_role_name,omitempty"`
	LeasePrefixes []string     `json:"lease_prefixes,omitempty"`
	ExtraGrants   []PathGrant  `json:"extra_grants,omitempty"`
	// CredPath is the credential read path the generated policy grants and the
	// job-credential template reads: dynamic = "<mount>/<creds_path>" (== the lease
	// prefix when lease-based), static = the KV v2 DATA path. Empty for wif-token
	// (the WIF token itself is the credential) and for records persisted before
	// this field existed (readers fall back to LeasePrefixes[0]).
	CredPath string `json:"cred_path,omitempty"`
	// KVPaths are KV v2 relative paths (under the executor's KV mount) whose
	// metadata+versions Deprovision deletes — the static source's seeded secret.
	// Absent on legacy records (their secrets predate this cleanup).
	KVPaths []string `json:"kv_paths,omitempty"`
}
