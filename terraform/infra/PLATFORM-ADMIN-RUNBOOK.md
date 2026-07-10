# Platform-Admin Onboarding Plane — Apply & Live-Verification Runbook

Operator runbook for turning on the Platform-Admin onboarding plane and verifying it
on the live platform. Everything here is **additive and gated** on
`enable_platform_admin`; with the flag `false` (default) the portal is byte-identical
to the developer-only build, and the plane degrades gracefully if it can't initialize.

Run from `terraform/infra/`. The apply mutates the live platform, so it is an
operator-gated step (needs AWS creds, the Vault root token, and NLB operator-CIDR
reachability).

---

## 0. Prerequisites (verified in code — no action unless noted)

- `secret/infra/mcp-gateway` already holds `jwt_secret_key` + `admin_email`
  (`mcp-gateway.tf`). The portal mints the ContextForge admin JWT from these.
- LiteLLM already has its own Postgres, so `STORE_MODEL_IN_DB=true` is safe.
- **Node pool choice:** portal-deployed MCP servers default to node pool `agents`
  (`platform_admin_mcp_node_pool = "agents"`), which needs `enable_agent_nodes=true`.
  To run them on the all-in-one node instead, set `platform_admin_mcp_node_pool = ""`.
- **Portal image must include the admin-plane code (ACTION REQUIRED).** The
  `developer-portal` job pulls `var.developer_portal_image` (default
  `panchalravi/developer-portal:poc`, `force_pull=true`). The admin plane lives in the
  portal binary (`internal/{rbac,admin,store,mcpgw,llmgw}`), so the image must be built
  from current code **before** this apply — otherwise the infra applies cleanly but the
  portal logs neither "plane enabled" nor "disabled", `/api/admin/*` returns **404**, and
  §2 fails. Rebuild + push, then restart the job to pull it:
  ```bash
  PORTAL_IMAGE=panchalravi/developer-portal:poc portal/scripts/build-image.sh
  nomad job restart -namespace infra -reschedule -on-error=fail developer-portal
  ```
  (A mutable `:poc` tag needs the explicit restart; a pinned tag would change
  `var.developer_portal_image` + re-apply instead.)
- **Blueprint validation is static — shape + policy-lint only (no Vault, no action).**
  Publishing a blueprint runs two checks: manifest shape, then render + lint of the generated
  least-privilege policy against the allowed path prefixes. There is **no** live consumption-mirror
  probe — runtime correctness of a recipe is the platform admin's responsibility, verified by
  deploying the A/B/C reference blueprints into a real project. The canonical manifest is stored
  **on the Portal DB row** (postgres `blueprints.data`), not in Vault KV, so no `infra/blueprints/*`
  Vault grant is needed.
- **Blueprints are immutable; delete is unreferenced-only.** A change to a published recipe is a
  new version (never an in-place edit). A blueprint can be deleted only while **no** MCP server type
  binds it — the portal returns **409** ("bound by server type …") otherwise; unbind/delete the
  server type first.
- **Verify RBAC group (manual — ACTION REQUIRED).** In IBM Verify, create a group named
  exactly `platform-admins`, add the platform-admin user(s) to it, and confirm the
  portal's OIDC app releases the `groups` claim carrying the group's **name** (not a
  UUID/DN). This is the *only* thing that grants the `platform-admin` role — matched
  case-insensitively in `portal/backend/internal/rbac/rbac.go` (`platform-admins`,
  singular `platform-admin` does **not** match). The `groups` claim is emitted by the
  portal's Verify OIDC app (now Terraform-created — see next bullet). Without this group,
  §2's `/api/me` returns an empty `roles` and every `/api/admin/*` route 403s.
- **Portal Verify OIDC app is created by Terraform — but free a slot first (ACTION REQUIRED
  once).** The portal app (`secured-codespace-portal`) is no longer hand-registered: the
  identity module creates it when `enable_developer_portal = true`
  (`modules/identity/verify.tf`), derives its redirect URI from the live NLB
  (`https://<nlb_dns_name>:8443/auth/callback`), emits the `groups` + `may_act` claims, and
  wires its generated client id/secret into the portal automatically. Because the redirect
  URI is derived on every apply, it **self-heals on rebuild** — the `CSIAQ0167E ...
  redirection URI ... does not match` error no longer happens (it was caused by a
  hand-registered URI going stale when the NLB DNS changed). **One-time migration:** the
  tenant enforces a **5-application cap**, and a full stack already uses 4 (boundary, nomad,
  + token-exchange/other). So **delete any pre-existing hand-registered
  `secured-codespace-portal` app** before applying, or the create fails with
  `CSIAD0030 (exceeded the allowed limit of 5 applications)`. The access-token `audiences`
  are set by `var.portal_oidc_audiences`; only `var.portal_oidc_issuer` still needs setting.

---

## 1. Apply

```bash
cd terraform/infra
terraform plan  -var enable_platform_admin=true -var enable_agent_nodes=true
terraform apply -var enable_platform_admin=true -var enable_agent_nodes=true
```

What this changes (all additive):

| Resource | Effect |
|---|---|
| `nomad_namespace.infra_mcp` | creates the `infra-mcp` namespace for portal-deployed MCP servers |
| `vault_policy.infra_platform_admin` | grants the portal WIF role: read `mcp-gateway` + `llm-gateway`, rw `llm-providers/*`, write `mcp-servers/*` (**never** the LiteLLM master key) |
| `terraform_data.litellm_portal_admin_key` | runs `scripts/litellm-portal-admin-key.sh` → mints a **proxy-admin** (non-master) key → `secret/infra/llm-gateway.portal_admin_key` |
| `litellm.nomad.hcl.tftpl` | flips `STORE_MODEL_IN_DB=true` (redeploys the litellm job) |
| `nomad_acl_policy.portal_service_discovery` | job-scoped read of `infra`-namespace Nomad services, bound to the `developer-portal` workload identity (gateway discovery) |
| `developer-portal` job | redeploys with `PORTAL_MCP_NAMESPACE=infra-mcp`, `PORTAL_AGENT_NODE_POOL=agents`, and a `nomadService` template that resolves `PORTAL_MCP_GATEWAY_ADDR` / `PORTAL_LLM_GATEWAY_ADDR` from the `mcp-gateway` / `llm-gateway` Nomad services at runtime (no loopback/co-location requirement) |
| portal **`vault-provisioner`** workload identity | gives the portal task a second Nomad workload identity (`aud = vault-provisioner`, JWT at `secrets/nomad_vault_provisioner.jwt`), so a **project-admin** can instantiate a credential blueprint into their project's Vault namespace. At deploy time the portal trades that identity at the **target namespace's** `jwt-nomad` backend (role `portal-provisioner`) for a short-TTL (300s) token **native to that namespace**, which evaluates a **relative-path** `portal-provisioner` policy — so the portal holds **no standing token** into any project and the old root-token `+/` namespace-prefix ACL problem never arises. The role + policy are created per-namespace by the **project tier** (`terraform/project/portal-provisioner.tf`), so each project must have had its `terraform/project` apply run first. See `terraform/infra/README.md`. **Enables the project-admin deploy plane (§5).** |

> If the key-bootstrap `local-exec` can't reach the gateway, the apply still
> succeeds; the portal logs a warning and disables only the admin plane. Manual
> fallback: create a `proxy_admin` key in the LiteLLM Admin UI and
> `vault kv patch secret/infra/llm-gateway portal_admin_key=<key>`.

---

## 2. Confirm the plane initialized

- Portal boot log: `admin plane: using in-memory control-plane store …` (or postgres,
  once `PORTAL_DB_DSN` is wired — see the (c) follow-up). A warning + disabled plane
  means a Vault read failed; check the `infra-platform-admin` policy attach.
- **Gateway service discovery** — the portal resolves the gateway addresses from Nomad
  services (not loopback). Confirm both resolve:
  ```bash
  nomad service info -namespace infra mcp-gateway   # expect 1 healthy instance
  nomad service info -namespace infra llm-gateway
  ```
  If the portal log shows the plane disabled with empty gateway addresses, the
  `nomadService` lookup returned nothing — verify the `infra-portal-service-discovery`
  ACL policy applied and is bound to the `developer-portal` job (`nomad acl policy info
  infra-portal-service-discovery`). The gateways must be in the **same namespace**
  (`infra`) as the portal for the workload-identity read to resolve.
  > **Startup-ordering race (after a full destroy+recreate).** If the portal task
  > starts *before* the `mcp-gateway`/`llm-gateway` services finish registering, its
  > `gateways.env` template renders **empty**, and because that template is
  > `change_mode = "noop"` (chosen to avoid a gateway-flap restart loop) the portal
  > **never re-reads** the addresses once the gateways come up — so the onboarding
  > plane stays **disabled** with `engineProvisioner`/`projectEng`/`projectTmpl`
  > unwired (symptoms: project-create skips engine auto-provisioning → empty
  > `credential_library_id` + gray checkmarks; GitHub-credentials save no-ops;
  > "New template" button disabled). **Fix: once both gateway services are healthy,
  > restart the portal once so it re-renders with populated addresses:**
  > ```bash
  > nomad job restart -namespace infra -on-error=fail developer-portal
  > ```
  > Confirm the boot log now reads `platform-admin onboarding plane enabled` with
  > non-empty `mcp_gateway`/`llm_gateway`. Any project created while the plane was
  > disabled must be **deleted and recreated** so creation auto-provisions its engines.
- Identity / RBAC:
  ```bash
  curl -s https://<portal>/api/me        # platform-admin user → "roles":["platform-admin"]
  ```
  A **non**-`platform-admins` user hitting any `/api/admin/*` route must get **403**
  (`RequirePlatformAdmin`). Verify this explicitly.

---

## 3. MCP servers — now project-owned (Phase F)

The platform-admin MCP plane (reference deploys in `infra-mcp`, consumption-mirror
test, publish-to-catalog, Vault blueprints) was **removed**. A **project-admin**
authors and deploys MCP servers directly from *project switcher → mcp servers →
Deploy MCP server*: one wizard holds the server definition (image, transport,
container port, args, env) and the credential config (none / Vault WIF token /
static secret / dynamic engine — PostgreSQL and AWS presets prefill the form).
Jobs run in the **project's own Nomad namespace** with **dynamic host ports** (no
8080–8099 static band, no cross-project collisions); credentials are brokered into
the **project's Vault namespace** over the provisioner workload identity; the
per-row **Test** button still runs the consumption-mirror (tools discovered,
own-token 200, admin 403, decoy 403).

Residuals from the retired plane are harmless: the `mcp_servers` + `blueprints`
Postgres tables are kept-but-unrouted (ops may `DROP` them), the
`secret/data/infra/mcp-servers/*` grant in `platform-admin.tf` is orphaned, and
any reference jobs still running in `infra-mcp` can be purged
(`nomad job stop -namespace infra-mcp -purge <job>`).

---

## 4. LLM live verification (consumption-mirror)

Drive it from the **LLM models** entry in the left nav (platform-admins only →
`/admin/llm-models`). The provider key is written write-only to
`secret/infra/llm-providers/<provider>` and injected at call time — never stored in the
control-plane DB or displayed again.

1. **Set the provider key (do this first).** In the **Set a provider API key** form,
   enter **Provider** (e.g. `deepseek`) and **API key** (`sk-…`), then click **Save key**.
   Required before onboarding that provider's first model — without it onboarding/test
   has no upstream credential.
2. **Onboard** — in the onboard form enter **Model name** (e.g. `deepseek-v4-flash`),
   **Provider** (`deepseek`), and **Backend model** (e.g. `deepseek/deepseek-chat`), then
   click **Onboard** (`/model/new`; the provider key is pulled from Vault at call time).
   The model appears under **Gateway inventory** with status **draft**.
3. **Test** — click **Test** on the row. The portal mints a **scoped key**
   (`max_budget` + `rpm_limit`) → completion **200** → exceed `rpm_limit` → **429** →
   revoke key → **401**. The **Test** column turns green: **passed**.
4. **Publish** — click **Publish** (enabled once the test passes). The model joins the
   project allow-list. **Delete** removes a managed model.

**Pass:** key saved → onboard (**draft**) → **Test** green (200 / 429 / 401 under the
hood) → **Publish** flips status to **published**.

### 4a. Verify the provider/model directly in LiteLLM

The portal **Test** uses a scoped budgeted key; you can also confirm the model landed in the
gateway and that the provider key actually reaches DeepSeek. LiteLLM runs as the `llm-gateway`
Nomad job in `infra`, **plaintext HTTP on :4000**, reachable via the NLB locked to the operator
`/32` — get its base URL from `terraform output -raw llm_gateway_addr` (`http://<nlb>:4000`).
Admin auth is the proxy **master key** in Vault. This is the same surface as the deeper
[Verify the LLM Gateway](./README.md#verify-the-llm-gateway) walk in the infra README.
```bash
MASTER=$(vault kv get -field=master_key secret/infra/llm-gateway)   # starts with sk-
LLM="$(terraform output -raw llm_gateway_addr)"                     # http://<nlb>:4000 (operator /32)
```

**UI.** Open `$(terraform output -raw llm_gateway_addr)/ui` and sign in with the master key (username `admin`,
master key as the password). **Models** lists the onboarded model; **Virtual Keys** shows the
portal-admin key + per-project virtual keys; **Usage/Logs** shows spend. The model appearing
here = LiteLLM has it in its DB-backed model list (`STORE_MODEL_IN_DB=true`).

**API.**
```bash
# the onboarded model is served
curl -s "$LLM/v1/models"  -H "Authorization: Bearer $MASTER" | jq '.data[].id'
# its provider/backend mapping
curl -s "$LLM/model/info" -H "Authorization: Bearer $MASTER" | jq '.data[] | {model_name, litellm_params}'
# the provider key actually reaches the upstream (pings each model's backend)
curl -s "$LLM/health"     -H "Authorization: Bearer $MASTER" | jq '{healthy: .healthy_count, unhealthy: .unhealthy_count}'
# end-to-end completion through the gateway with the master key
curl -s "$LLM/v1/chat/completions" -H "Authorization: Bearer $MASTER" -H 'Content-Type: application/json' \
  -d '{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"ping"}],"max_tokens":8}' | jq '.choices[0].message.content'
```

**Pass:** the model appears in `/v1/models` + `/model/info`, `/health` shows it healthy
(provider key valid), and the completion returns content.

---

## 4b. Create a project from the Portal (project-create plane)

The §1 apply also enables the **project-create plane** (`enable_platform_admin = true` +
`enable_developer_portal = true`): the portal task gets its third workload identity (`aud =
vault-creator`) and the `project-creator` Vault role, a dedicated Nomad token, and the F-A Boundary
account. A platform-admin can now **create a project from the Portal** instead of running
`terraform/project` by hand — from the **Projects (admin)** page (left nav): click **New project**,
fill project name (`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`), the IBM Verify developers group, the
workspace user, and the first project-admin's email, then **Create project**. Equivalent API:

```bash
curl -sk -X POST "$PORTAL/api/admin/projects" -H "Cookie: portal_session=<value>" \
  -H 'Content-Type: application/json' \
  -d '{"project_name":"project-acme","developers_group_name":"project-acme-developers","workspace_user":"dev","first_admin":"admin@org.com"}'
```

This creates the Vault child namespace (+ `jwt-nomad`, project KV, `portal-provisioner` role/policy,
workspace WIF role), the Nomad namespace + `project-acme-dev` policy + binding rule, the Boundary
project scope, the root-KV descriptor, and grants the first project-admin. **Prereqs:** the first
admin must already be in the `project-acme-developers` Verify group; **per-project engines
(SSH/GitHub/DB) and job-templates are set up afterward** (Phase 3) — a freshly created project lists
with zero flavors until then.

> **First-apply live checks:** confirm (a) the new Vault namespace has `jwt-nomad` +
> `portal-provisioner` role/policy (validates the `project-creator` `+/…` cross-namespace globs), and
> (b) a workspace still launches (validates the F-A Boundary grant set — see the infra README's
> project-create-plane note). If a partial create fails mid-way, re-run the same call — every step is
> idempotent and converges.

## 5. Project-admin MCP deploy — prerequisites

No platform-admin publish/blueprint steps exist anymore (§3). For a project-admin
to deploy MCP servers, the platform needs only:

1. **This plane applied** (§1) — the portal's `vault-provisioner` workload identity.
2. **A project created from the Portal** (§4b) — project-create seeds the
   per-namespace `portal-provisioner` role/policy the broker logs into.
3. *(Class A demo only)* the demo Postgres: `enable_demo_db = true` →
   `terraform output demo_db_connection_url` / `-raw demo_db_admin_password`.

Everything else — authoring the credential spec, deploying, testing, attaching the
server to a workspace template via Add-ons — happens in the project-admin UI. See
the walkthrough (`docs/e2e-clean-slate-walkthrough-2026-07-02.md` Part 2) for the
end-to-end script.

---

## 6. Regression gate (must stay green)

Confirm the `STORE_MODEL_IN_DB` flip and the portal redeploy didn't disturb existing
flows:

- Developer login → list projects → create / stop / SSH a workspace.
- MCP/LLM secrets present in the workspace; config-source models
  (`deepseek-v4-pro` / `deepseek-v4-flash`) still served; workspace Claude Code
  traffic unaffected.

---

## 7. Rollback

```bash
terraform apply -var enable_platform_admin=false
```

Drops the env/policy/namespace and reverts `STORE_MODEL_IN_DB`; the portal returns to
byte-identical developer-only behavior. This also drops the portal's second
`vault-provisioner` workload identity, so the portal can no longer broker a
provisioner token into any project namespace and the
**project-admin deploy plane (§5) becomes unavailable** — the per-namespace
`portal-provisioner` role/policy remain in each project until that project's tier is
destroyed. Already-deployed project MCP
servers + their Vault state are untouched (delete them via the project-admin runbook
first if you want a clean teardown). Models already persisted in LiteLLM's Postgres
remain there but are no longer managed by the portal.

---

## After verification — (c) follow-up to make the store durable

The onboarding plane currently uses the in-memory control-plane store (the Nomad jobs,
gateway peers, and LiteLLM models persist in their own systems regardless). To make the
portal's own state durable, the **`store.Postgres`** impl is already built and verified
and is selected automatically when `PORTAL_DB_DSN` is set. The remaining infra is a
`portal-postgres` Nomad job + a Vault DB dynamic role (`portal-app`) that renders the
DSN into the portal job env — to be added next.

## Durable workspace storage (EBS CSI)

Workspace `/home/dev` is a durable **per-workspace AWS EBS volume** provisioned through the
**AWS EBS CSI driver on Nomad** (replacing the node-local `mkdir` host volume, which was lost
on instance replacement). The volume survives node crash / instance replacement and reattaches
to a replacement node in the same AZ. Only workspace volumes use CSI; the `mkdir` stores
(portal/LiteLLM Postgres, MCP SQLite) are unchanged.

Pieces: `ebs-csi.tf` + `templates/ebs-csi-{controller,node}.nomad.hcl.tftpl` (driver jobs in
`infra`); `modules/secured-codespace/iam.tf` (instance profile — the instances had none before);
`allow_privileged = true` on the docker driver in every node's Nomad config (the node plugin
stages block devices); `internal/hashistack/nomad.go` (`CreateHostVolume`/`DeleteHostVolume`
now drive the Nomad CSI API); the three workspace seed jobspecs (`volume "home"` → `type = "csi"`);
and `modules/secured-codespace/backup.tf` (daily AWS Backup, selected by the `backup=secured-workspace`
tag the driver stamps on each volume; toggle `enable_workspace_backups`).

**ACTION REQUIRED — apply via a clean destroy + recreate, not `terraform apply` on the running
node.** `aws_instance.this` has `lifecycle { ignore_changes = all }` and `config/nomad.hcl` is the
cloud-init source only, so the new instance profile and `allow_privileged` reach a node **only at
first boot**. Rebuild sequence:

```bash
awscreds                                   # refresh AWS creds (doormat); run via `! awscreds` if interactive
# destroy (reverse-create order), each with a clean env:
for tier in workspace project infra; do
  ( cd ../$tier 2>/dev/null || cd terraform/$tier
    env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
        -u VAULT_ADDR -u VAULT_TOKEN -u NOMAD_ADDR -u NOMAD_TOKEN \
        terraform destroy -auto-approve )
done
# recreate: infra first (nodes boot with the profile + allow_privileged + CSI jobs), then re-onboard
env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
    -u VAULT_ADDR -u VAULT_TOKEN -u NOMAD_ADDR -u NOMAD_TOKEN \
    terraform apply -auto-approve
```

**Verify:**

```bash
nomad plugin status aws-ebs                 # Controllers 1/1, Nodes N/N healthy
# create a workspace from the Portal, then:
nomad volume status -namespace <project-ns> # the home-<handle>-<ws> CSI volume, Schedulable
# an EBS vol-… tagged Name=home-… + backup=secured-workspace appears in the EC2 console
# SSH in → `df -h /home/dev` shows a ~20G ext4 EBS device (e.g. /dev/nvme1n1, NOT the root fs)
# write a file, force a reschedule (drain/replace the node), reconnect → the file survives
```

**ACTION REQUIRED — the deployed portal image must carry the CSI code, and the base
templates must be re-seeded.** The `type = "csi"` volume stanza lives in the portal's
**embedded seed HCL** and the CSI provisioning lives in `internal/hashistack/nomad.go`, so a
portal image built *before* this change still creates `mkdir` volumes. After building +
pushing an image that contains the CSI code (`portal/scripts/build-image.sh`, pin the tag in
`portal.auto.tfvars`, `terraform apply -target=nomad_job.developer_portal`), refresh the base
templates — `SeedBaseTemplates` **never overwrites an existing row** (`jobtemplate/seeds.go`),
so the shipped `type = "host"` rows persist until deleted:

```bash
# 1. delete the shipped base-template rows (predicated — an unpredicated wipe is blocked):
nomad alloc exec -namespace infra -task postgres <portal-postgres-alloc> \
  psql -U portal -d portal -c \
  "DELETE FROM base_job_templates WHERE name IN ('dev-workspace','gpu-workspace','microvm-workspace');"
# 2. restart the portal so it re-seeds them from the embedded type=csi HCL:
nomad job restart -namespace infra -on-error=fail developer-portal
# 3. project-admin: delete + recreate each project template (re-bakes from the csi base)
# 4. developer: delete the old mkdir workspace + create a new one → it gets a CSI/EBS volume
```

Verified live 2026-07-10 (portal `:agentv14`): a Portal-created workspace produced CSI volume
`home-alice-wmj4d` backed by EBS `vol-…` (gp3, 20 GiB, encrypted, `backup=secured-workspace`),
mounted at `/home/dev` on `/dev/nvme1n1`.
