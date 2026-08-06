# Platform internals — how the pieces fit, and what bites

Reference notes for whoever operates this tier. **Not a procedure**: stand the platform up with
[`README.md`](./README.md), exercise it with [`E2E-WALKTHROUGH.md`](./E2E-WALKTHROUGH.md). This file
explains the parts of `terraform/infra/` whose behaviour is not obvious from the HCL, and the traps
that only surface on an upgrade or a rebuild.

---

## The platform-admin plane

`enable_platform_admin = true` turns the Portal from a developer-only app into the control plane for
the whole platform. It is what makes project-create, LLM-model onboarding and project-owned MCP
deploys possible at all.

| Resource | Effect |
|---|---|
| `vault_policy.infra_platform_admin` | the Portal's WIF role may read `mcp-gateway` + `llm-gateway`, read/write `llm-providers/*`, write `mcp-servers/*` — **never** the LiteLLM master key |
| `terraform_data.litellm_portal_admin_key` | runs `scripts/litellm-portal-admin-key.sh` → mints a **proxy-admin** (non-master) LiteLLM key → `secret/infra/llm-gateway.portal_admin_key` |
| `litellm.nomad.hcl.tftpl` | flips `STORE_MODEL_IN_DB=true`, so onboarded models persist in LiteLLM's own Postgres |
| `nomad_namespace.infra_mcp` | the `infra-mcp` namespace (a residual — see *Retired surfaces*) |
| `portal-postgres` job | the Portal's durable control-plane store |
| portal **`vault-provisioner`** identity | second workload identity (`aud = vault-provisioner`) — lets the Portal broker a short-TTL token *native to a project's Vault namespace* |
| portal **`vault-creator`** identity | third workload identity (`aud = vault-creator`) + the `project_creator` policy — lets the Portal create child namespaces |

### Three identities, no standing tokens

The Portal never holds a long-lived token into any project. It carries three Nomad workload
identities (`templates/developer-portal.nomad.hcl.tftpl:50,62`):

- the **default** one, for its own `infra` reads (`vault_policy.infra_portal_read`);
- **`vault-provisioner`**, traded at the *target namespace's* `jwt-nomad` backend (role
  `portal-provisioner`, TTL 300s) for a token native to that namespace, evaluating a **relative-path**
  policy. This is what lets a project-admin deploy an MCP server with brokered credentials;
- **`vault-creator`**, for creating a project's child namespace at project-create.

The per-namespace `portal-provisioner` role and policy are seeded **by the Portal itself** during
project-create (`developer-portal.tf:131`) — there is no Terraform step per project.

### The `portal_admin_key` is a hard dependency

`terraform_data.litellm_portal_admin_key` mints a LiteLLM `proxy_admin` key for the service account
`portal-admin` and writes it to Vault. This is **not** best-effort:

- the resource has **no** `on_failure = continue`, so a failed script fails the apply;
- the Portal reads the key at boot and **returns an error if it is missing**
  (`portal/backend/cmd/portal/main.go:136`), so the task restart-loops rather than starting degraded.

The script polls the gateway's `/health/liveliness` through the NLB for 60s. On a cold build the
LiteLLM alloc can be running while the NLB target group has not yet passed its own TCP check on
`:4000` — which is the usual cause of failure. Detection and the manual fallback are in
[README §7](./README.md#7-troubleshooting).

> Any comment in the HCL claiming this "degrades gracefully" or "never blocks a deploy" is wrong and
> predates the Portal's boot-time check.

### Gateway addresses are static

`PORTAL_MCP_GATEWAY_ADDR` / `PORTAL_LLM_GATEWAY_ADDR` are pinned to the node's stable gateway ports
in the jobspec (`templates/developer-portal.nomad.hcl.tftpl:160-161`). They were once rendered from a
`nomadService` discovery template, which raced the gateways on a cold rebuild: the template rendered
empty, `change_mode = "noop"` meant it was never re-read, and the onboarding plane stayed silently
disabled. **That race is fixed** — if you find a doc telling you to "restart the portal once" after a
rebuild, it is stale. The symptoms are still worth recognising, and are in
[README §7](./README.md#7-troubleshooting).

### Rollback

```bash
tf apply -var enable_platform_admin=false
```

Drops the policy, the extra identities and `STORE_MODEL_IN_DB`; the Portal reverts to developer-only
behaviour. Existing projects keep their `portal-provisioner` role and policy (they live in each
project's Vault namespace), already-deployed MCP servers keep running, and models already in
LiteLLM's Postgres stay there — simply unmanaged. Delete projects from the Portal first if you want a
clean teardown.

---

## Control-plane state (portal-postgres)

The Portal's own state — project descriptors, base and project templates, roles and capability
matrices, MCP-server rows, audit events — lives in a dedicated `portal-postgres` job
(`portal-postgres.tf`, node-static port **15434**; `demo-db` is 15432, LiteLLM's Postgres 15433).

`store.Postgres` is selected automatically when `PORTAL_DB_DSN` is set; `developer-portal.tf` copies
the generated password into the Portal's KV so the job assembles the DSN over its existing WIF read.
Without the DSN the Portal falls back to an **in-memory** store — Nomad jobs, gateway peers and
LiteLLM models still persist in their own systems, but every Portal row is lost on restart. Gated on
`enable_developer_portal && enable_platform_admin`.

Check the boot log for `control-plane store: postgres`.

---

## Upgrading the Portal image — the seed trap

**This is the one that silently does nothing.** Base templates ship embedded in the Portal binary,
but `SeedBaseTemplates` **never overwrites an existing row** (`jobtemplate/seeds.go:83`) — by design,
so a platform-admin's edits survive a restart. The consequence: deploying a new image whose *embedded
seed HCL* changed has **no effect** on a platform that already ran. The old rows persist, and every
template baked from them keeps the old behaviour.

This has bitten twice — the EBS CSI change (`volume "home"` moving from `type = "host"` to
`type = "csi"`) and the host-sync change (the tagged `service{}` stanza). If a change touches the
embedded seeds, re-seed after deploying:

```bash
# 1. delete the shipped rows (a predicated DELETE — an unpredicated wipe is blocked)
nomad alloc exec -namespace infra -task postgres <portal-postgres-alloc> \
  psql -U portal -d portal -c \
  "DELETE FROM base_job_templates WHERE name IN ('dev-workspace','gpu-workspace','microvm-workspace');"

# 2. restart the Portal so it re-seeds from the new embedded HCL
nomad job restart -namespace infra -on-error=fail developer-portal

# 3. project-admin: delete and recreate each project template (re-bakes from the new base)
# 4. developer: delete and recreate each workspace (templates bind at launch)
```

A **mutable** image tag (`:poc`) also needs `nomad job restart … developer-portal` to pull. A **new**
tag is a `developer_portal_image` change in tfvars plus
`tf apply -target=nomad_job.developer_portal`.

---

## Durable workspace storage (EBS CSI)

Workspace `/home/dev` is a per-workspace **EBS volume** provisioned through the AWS EBS CSI driver on
Nomad, replacing a node-local `mkdir` host volume that was lost whenever the instance was replaced.
The volume survives node crash and reattaches to a replacement node **in the same AZ**. Only
workspace volumes use CSI — the `mkdir` stores (Portal/LiteLLM Postgres, MCP SQLite) are unchanged.

Pieces: `ebs-csi.tf` + `templates/ebs-csi-{controller,node}.nomad.hcl.tftpl`;
`modules/secured-codespace/iam.tf` (the instance profile — the nodes had none before);
`allow_privileged = true` in `config/nomad.hcl:29` (the node plugin stages block devices);
`CreateHostVolume`/`DeleteHostVolume` in `internal/hashistack/nomad.go` driving the Nomad CSI API; and
`modules/secured-codespace/backup.tf` — daily AWS Backup with 30-day retention, selecting volumes by
the `backup=secured-workspace` tag the driver stamps on each one (`enable_workspace_backups`).

**The instance-profile trap.** `aws_instance.this` carries `lifecycle { ignore_changes = all }`, and
`config/nomad.hcl` is a cloud-init source, so the instance profile and `allow_privileged` reach a node
**only at first boot**. Adding them to a running stack requires replacing the node — see
[README §7](./README.md#7-troubleshooting) ("A config change did not reach the running node").

Verify with `nomad plugin status aws-ebs` (Controllers 1/1, Nodes N/N), then create a workspace and
confirm `df -h /home/dev` shows a ~20G EBS device rather than the root filesystem.

> The AWS Backup vault refuses to delete while it holds recovery points — a destroy-time snag, see
> [README §11](./README.md#11-destroy).

---

## Workspace reachability (`nomad-boundary-host-sync`)

Durable storage keeps `/home/dev` alive across a node change; this keeps the workspace **reachable**.
Boundary Enterprise cannot load a custom dynamic-host plugin (its plugin set is compiled in), so an
external reconciler does the same job through Boundary's API (`nomad-boundary-host-sync.tf`, job in
namespace `infra`).

- **Topology** — the Portal creates one shared static host catalog **`dev-workspaces`** per project
  scope at project-create, and per workspace a host-set plus an ssh target. The **target id is
  stable**: it is what the developer's `authorize-session` grant references. The sync only keeps the
  single host inside each host-set pointed at the workspace's current node address, driven by the
  workspace's Nomad-native service (`provider = "nomad"`, `address_mode = "host"`, tags
  `service-type=workspace` + `project=<ns>`). It never creates or deletes structure, and it never
  touches catalogs it did not create.
- **Credentials** — the scoped `developer-portal` Boundary account plus a read-only Nomad token
  (`nomad-boundary-host-sync-read`: `namespace "*"` → `list-jobs`/`read-job`), both read from Vault KV
  `infra/nomad-boundary-host-sync` over WIF.

Two behaviours worth knowing before you debug it:

- **Host-set lookups must `Read`, not `List`.** Boundary's host-set *List* API returns
  `host_ids: null`, so a List-only lookup never sees the host it just created and loops forever on a
  unique-name collision.
- **No periodic restart.** The KV secrets are static, so the job's `vault{}` and `template{}` stanzas
  use `change_mode = "noop"` — Nomad re-derives the WIF token at `token_max_ttl` (1h) without bouncing
  the task. (The `vault` stanza's *default* `change_mode = "restart"` was an earlier hourly-restart
  bug.) Because the task is long-lived, the reconciler instead re-authenticates to Boundary on
  session expiry: any call returning 401 triggers one re-auth and retry (`retryOnAuthExpiry` in
  `internal/boundary`). Rotating the Boundary password or Nomad token is a Terraform action, picked up
  at the next task start.

Workspaces created before this existed keep their old per-workspace catalogs and are **not**
address-synced; recreate them to move onto the current path. To exercise a cross-node move on a
single-node cluster, see `enable_default_spare` ([README §8](./README.md#8-optional-worker-nodes)).

---

## AI-agents plane substrate

Any project member with the **`ai-agents`** capability can author a YAML deep-agent, deploy it as a
Nomad service and chat with it, under the same tool/data and model governance a workspace gets. The
platform side is only the substrate:

- **`enable_agent_nodes = true`** stands up standard-CPU workers in Nomad node pool **`agents`**
  (`modules/secured-codespace/agent-nodes.tf`). The Portal places agent jobs there via
  `PORTAL_AGENT_NODE_POOL` — the same pool as project-deployed MCP servers — so neither ever loads the
  all-in-one node.
- **`agent-identity.tf`** provisions the RFC 8693 token-exchange plumbing for delegated agent
  identity, including the Vault identity-OIDC issuer that signs agent actor JWTs.
- **Runtime image** — deploys pull `agent-runtime` (deepagents + FastAPI), defaulting to
  `panchalravi/agent-runtime:agentv2` (`config.go:191`). It is a **Portal** default, not a Terraform
  variable, so the tag must exist in the registry. Each deploy mints a per-agent LiteLLM key, writes
  it to the project's Vault KV, grants the per-project `agents` WIF role, and registers the Nomad
  service `agent-<project>-<name>`.

Walkthrough: [`E2E-WALKTHROUGH.md`](./E2E-WALKTHROUGH.md) §4.

---

## Retired surfaces

Harmless residue from earlier phases. None of it is on a live path; listed so it is not mistaken for
something load-bearing.

- **The platform-admin MCP plane** — reference deploys into `infra-mcp`, publish-to-catalog, and
  Vault credential **blueprints** — was removed. A project-admin now authors and deploys MCP servers
  directly into their own project's Nomad namespace with dynamic host ports
  ([`E2E-WALKTHROUGH.md`](./E2E-WALKTHROUGH.md) §2.2).
- The `mcp_servers` and `blueprints` Postgres tables are kept but unrouted; ops may `DROP` them.
- The `secret/data/infra/mcp-servers/*` grant in `platform-admin.tf` is orphaned.
- Anything still running in `infra-mcp` can be purged:
  `nomad job stop -namespace infra-mcp -purge <job>`.
- `modules/credential-store-vault/` and `modules/ssh-secrets-vault/` are called by nothing.
- `terraform/project/` holds only documentation — its Terraform config was retired when project
  onboarding moved into the Portal. Its `PROJECT-ADMIN-RUNBOOK.md` still describes the removed
  blueprint flow.
