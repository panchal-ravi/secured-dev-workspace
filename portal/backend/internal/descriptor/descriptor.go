// Package descriptor models the per-project "portal descriptor" the project
// Terraform tier publishes to Vault KV (secret/projects/<project>/portal-descriptor)
// and the access rule that decides which projects a developer may see.
package descriptor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Feature is one capability a flavor provides, surfaced in the workspace card.
type Feature struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Flavor is one publishable job template the portal can read back. Label,
// Description, GitRepoURL and Image are surfaced to developers in the template
// picker; NodePool drives node placement (e.g. "gpu", empty = default pool); the
// Vault paths stay baked into the published template.
type Flavor struct {
	Name        string    `json:"name"`
	Label       string    `json:"label,omitempty"`
	Description string    `json:"description,omitempty"`
	GitRepoURL  string    `json:"git_repo_url,omitempty"`
	Image       string    `json:"image,omitempty"`
	NodePool    string    `json:"node_pool,omitempty"`
	Features    []Feature `json:"features"`
}

// Descriptor is the project contract the portal reads. It carries only what the
// portal needs that is NOT baked into the published job template: Boundary
// provisioning IDs, the access-control group, the node address, and per-flavor
// feature metadata. Field names match the JSON the project tier writes
// (terraform/project/portal.tf).
type Descriptor struct {
	ProjectName              string   `json:"project_name"`
	Namespace                string   `json:"namespace"`
	ProjectScopeID           string   `json:"project_scope_id"`
	CredentialLibraryID      string   `json:"credential_library_id"`
	DevelopersGroupName      string   `json:"developers_group_name"`
	BoundaryOIDCAuthMethodID string   `json:"boundary_oidc_auth_method_id"`
	InstancePrivateIP        string   `json:"instance_private_ip"`
	WorkspaceUser            string   `json:"workspace_user"`
	AliasSuffix              string   `json:"alias_suffix"`
	Flavors                  []Flavor `json:"flavors"`
	// GithubConfigured reports whether the project's GitHub App credentials have been
	// set (the github mount always exists after provision; the App config is supplied
	// later by a project-admin). Non-secret status flag — never the key material.
	GithubConfigured bool `json:"github_configured,omitempty"`
	// Non-secret GitHub App coordinates, persisted so the Engines page can prefill
	// its form on revisit. The private key is write-only and never stored here.
	GithubAppID             int      `json:"github_app_id,omitempty"`
	GithubAppInstallationID int      `json:"github_app_installation_id,omitempty"`
	GithubRepositories      []string `json:"github_repositories,omitempty"`
}

// Parse decodes the descriptor JSON string stored under the "descriptor" KV key.
func Parse(raw string) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Descriptor{}, fmt.Errorf("descriptor: parse: %w", err)
	}
	if d.ProjectName == "" {
		return Descriptor{}, fmt.Errorf("descriptor: missing project_name")
	}
	return d, nil
}

// AllowsGroups reports whether a developer in the given IBM Verify groups may
// access this project. Comparison is case-insensitive because IBM Verify lowers
// the groups claim while the descriptor carries the configured group name.
func (d Descriptor) AllowsGroups(groups []string) bool {
	want := strings.ToLower(strings.TrimSpace(d.DevelopersGroupName))
	if want == "" {
		return false
	}
	for _, g := range groups {
		if strings.ToLower(strings.TrimSpace(g)) == want {
			return true
		}
	}
	return false
}

// Flavor returns the named flavor, or false if the project has no such flavor.
func (d Descriptor) Flavor(name string) (Flavor, bool) {
	for _, f := range d.Flavors {
		if f.Name == name {
			return f, true
		}
	}
	return Flavor{}, false
}

// Visible filters all to the descriptors the given groups may access.
func Visible(all []Descriptor, groups []string) []Descriptor {
	out := make([]Descriptor, 0, len(all))
	for _, d := range all {
		if d.AllowsGroups(groups) {
			out = append(out, d)
		}
	}
	return out
}
