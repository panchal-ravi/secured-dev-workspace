//go:build live

package projectadmin_test

// Marquee end-to-end gate for the project-facing MCP deploy plane. Excluded from
// the default build (no `live` tag), so `go test ./...` never runs it; run it by
// hand against a reachable platform:
//
//	go test -tags live ./internal/projectadmin/ -run TestLiveDeployPostgresMCP -v
//
// Preconditions (operator):
//   - a platform-admin has published the Class A `postgres-mcp` blueprint and bound
//     its blueprint_ref onto a published `postgres-mcp` server type (B1 admin plane);
//   - the `portal-blueprint-provisioning` policy (terraform Task 8) is attached to
//     the portal's WIF role and the onboarding plane is enabled;
//   - `demo-db` (Postgres) is reachable from the agents node pool;
//   - `acme-admin@…` is a member of `project-acme-developers` AND holds the
//     `project-admin` role for `project-acme` (RBAC Plan A bootstrap).
//
// Steps (spec §6 marquee):
//  1. acme-admin POST /api/projects/project-acme/mcp-servers {server_type:postgres-mcp}
//     → 201; assert the database engine is mounted in the project namespace and
//     dynamic creds mint against demo-db.
//  2. POST …/postgres-mcp/test → consumption-mirror passes (own server 200, decoy +
//     admin 403); the gateway peer is registered, temp virtual servers torn down.
//  3. DELETE …/postgres-mcp → leases revoked BEFORE the engine is unmounted, the
//     Nomad job purged, the peer deregistered, the row dropped; no orphan Vault state.
//  4. Regression: a developer (no project-admin role) is 403 on all three routes; the
//     platform-admin deploy plane and workspace flows are unchanged.
//
// Kept a documented t.Skip stub here: the value is the build-tagged scaffold + the
// recorded preconditions/steps. Flesh out the HTTP calls only when running against a
// real environment.

import "testing"

func TestLiveDeployPostgresMCP(t *testing.T) {
	t.Skip("live gate — implement against real platform endpoints; see file header")
}
