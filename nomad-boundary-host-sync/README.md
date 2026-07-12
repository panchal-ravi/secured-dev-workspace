# nomad-boundary-host-sync

An external reconciler that keeps each workspace's **Boundary host address** pointed at
whatever **Nomad node** its alloc currently runs on, so the workspace's (stable) Boundary
target follows it across reschedules — the reachability half of durable workspaces (the
EBS-CSI home volume is the state half).

## Why this exists (and why it isn't a Boundary plugin)

Boundary Enterprise cannot load a custom dynamic-host plugin: the controller registers a
hardcoded, compile-time-embedded plugin set (aws/azure/gcp), and there is no runtime hook
to add one. So instead of a plugin *inside* Boundary, this is a small service *outside* it
that drives the same outcome through Boundary's API.

## Model

- **The portal owns all structure.** At project creation it makes one shared static host
  catalog (`dev-workspaces`) per project scope; at workspace create it makes a per-workspace
  host-set + ssh target (bound to that set) + per-developer grant + alias. The **target-id is
  stable** across reschedules — that's what a developer's `authorize-session` grant references.
- **This service owns only the dynamic host address.** Workspaces register a Nomad-native
  service (`provider = "nomad"`, `address_mode = "host"`) tagged `service-type=workspace` and
  `project=<namespace>`. Each reconcile pass:
  1. lists those services (current node IP + static SSH port);
  2. for each, resolves the Boundary project scope (by name = namespace) → shared catalog →
     the workspace's host-set (by name = job name);
  3. ensures the set contains exactly one host at the current address — creating it on first
     sight, updating it when the node changes. It **never** creates or deletes structure and
     never deletes hosts (the portal removes host-set + host at workspace destroy).

If the host-set doesn't exist yet (the Nomad service can register before the portal's
`Provision` runs), the pass skips that workspace and converges on a later cycle.

## Configuration (environment)

| Var | Required | Default | Meaning |
|---|---|---|---|
| `SYNC_NOMAD_ADDR` | yes | | Nomad API address |
| `SYNC_NOMAD_TOKEN` | no | | Nomad ACL token (needs `namespace "*" { policy = "read" }`) |
| `SYNC_BOUNDARY_ADDR` | yes | | Boundary controller address |
| `SYNC_BOUNDARY_AUTH_METHOD_ID` | yes | | password auth-method id |
| `SYNC_BOUNDARY_LOGIN` / `SYNC_BOUNDARY_PASSWORD` | yes | | admin account |
| `SYNC_BOUNDARY_ORG_SCOPE_ID` | yes | | parent scope of all project scopes |
| `SYNC_CATALOG_NAME` | no | `dev-workspaces` | shared per-project catalog name |
| `SYNC_CA_CERT_PATH` | no | | CA PEM (ignored when TLS verification is skipped) |
| `SYNC_TLS_SKIP_VERIFY` | no | `true` | set `false` to verify TLS |
| `SYNC_RECONCILE_INTERVAL` | no | `10s` | reconcile period |

## Run

```
go test ./...
go build ./cmd/sync
```

Deployed as its own `count = 1` Nomad job on the main node (co-located with the Boundary
controller + portal). See `terraform/infra` for the job + Vault credential wiring.
