# `internal/projectadmin`

The **project-facing** MCP onboarding plane. A project-admin deploys a published,
blueprint-backed MCP server type into **their own project**, with credentials
brokered by a platform-authored credential blueprint — never pasted.

It mirrors `internal/admin` (the platform-admin plane) but targets the **project**
Vault + Nomad namespace (`descriptor.Namespace`) and uses a **WIF credential**
(blueprint `InstanceRecord`) instead of KV `secret_refs`. It reuses the B1 engine
end to end: `blueprint.Executor.Instantiate/Deprovision`, `mcpjob.Render` (WIF
variant) + `mcpjob.CredentialEnv`, and the `project_mcp_servers` store record.

## Routes

All under `RequireProjectRole("project-admin")` — which passes iff the caller is a
**live member** of the project (Verify developer group, via the project lookup) **and**
holds a `project-admin` grant in the Portal DB. Mutations additionally go through the
project-mutate guard.

| Method | Path | Service method |
|---|---|---|
| `GET`    | `/api/projects/{name}/mcp-servers`            | `ListDeployable` |
| `POST`   | `/api/projects/{name}/mcp-servers`            | `DeployServer`   |
| `POST`   | `/api/projects/{name}/mcp-servers/{server}/test` | `TestServer`  |
| `DELETE` | `/api/projects/{name}/mcp-servers/{server}`   | `DeleteServer`   |

## Deploy flow (and rollback)

`DeployServer` resolves the project (membership + namespace) → loads the published
server type and re-asserts its bound blueprint's content hash → guards against a
duplicate deploy (409) → `Instantiate`s the blueprint into the project Vault
namespace → renders a Nomad job bound to the **minted WIF role** with the blueprint's
credential env-template → registers the job → persists a `project_mcp_servers` row.

`Instantiate` validates the manifest + required params itself, so a failure *there*
left nothing to roll back. Any failure **after** a successful `Instantiate` (render,
register, placement, marshal, persist) triggers exactly one best-effort
`Deprovision(rec)` (+ `PurgeJob` once a job exists), so a project never accrues orphan
Vault state. `DeleteServer` deprovisions the **persisted** `InstanceRecord`, so teardown
matches what deploy created; the executor revokes dynamic leases **before** unmounting
the engine.

## Vault grant

At runtime the portal authenticates to Vault as its **root** WIF identity and scopes
each call to the project namespace via `client.WithNamespace(<project>)`. The grant is
the `portal-blueprint-provisioning` policy (`terraform/infra/portal-provisioning-policy.hcl`),
attached to that root role and gated on `enable_platform_admin`. Containment is
application-level (always target a project namespace + generated-policy lint + explicit
deny stanzas), not physical per-namespace pinning — see `terraform/infra/README.md`.

## Scope deviation (flagged)

The standalone `internal/mcpdiscovery` package (spec §3.4 — join Nomad service-tags ∪
ContextForge peers) is **deferred**. `mcpgw.Client` has no `ListPeers` and the Nomad
client exposes no service-tag scan, while the `project_mcp_servers` rows already persist
`Status`/`PeerID`/`TestResult`. Deployed-server status is therefore derived from those
rows + `nomad.JobExists`.

## Tests

`service_test.go` / `handlers_test.go` cover the catalog join, deploy happy-path +
rollback + 404/409, the consumption-mirror test (200/403 + teardown), and delete
(deprovision-with-persisted-record + 404), over an in-memory store and fakes.
`live_test.go` (`//go:build live`) is the documented end-to-end skip-stub:

```bash
go test -tags live ./internal/projectadmin/ -run TestLiveDeployPostgresMCP -v
```
