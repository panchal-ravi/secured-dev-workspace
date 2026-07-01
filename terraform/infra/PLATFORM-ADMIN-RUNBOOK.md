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
- Identity / RBAC:
  ```bash
  curl -s https://<portal>/api/me        # platform-admin user → "roles":["platform-admin"]
  ```
  A **non**-`platform-admins` user hitting any `/api/admin/*` route must get **403**
  (`RequirePlatformAdmin`). Verify this explicitly.

---

## 3. MCP live verification (consumption-mirror)

Worked example: the HashiCorp Vault MCP server. Drive it from the **MCP servers** entry
in the left nav (shown only to platform-admins → `/admin/mcp-servers`).

1. **Deploy** — click **Deploy MCP server** (top-right) and fill the modal from the
   server's container run docs:
   - **Name** — lowercase letters/digits/dashes, e.g. `vault-mcp`.
   - **Container image** — e.g. `hashicorp/vault-mcp-server:latest`.
   - **Transport** — `SSE` or `Streamable HTTP`.
   - **Listen port** — the port the server listens on in the container. **Must be in
     `8080`–`8099`**: MCP jobs run on the `agents` node pool and the ContextForge gateway
     (main node) dials them cross-node at `nodeIP:<port>`; only that band is opened
     intra-SG on the agent nodes (`self=true`, `network.tf`). A port outside it deploys
     but Test/Publish fail with `ConnectTimeout` (the gateway's dial is dropped), so the
     portal rejects out-of-band ports with 400.
   - **MCP path** (optional) — defaults to `/sse` or `/mcp` by transport.
   - **Arguments** (optional, one per line) and **Environment** (optional, `KEY=VALUE`
     per line — **non-secret values only**; secrets are injected from Vault).
   - **Inject a Vault (WIF) token as `VAULT_TOKEN`** (checkbox) — tick it for servers
     that authenticate to Vault to open a session (e.g. the Vault MCP server). The
     portal renders a bare `vault { role }` block on the job and **Nomad injects a
     powerless self-test `VAULT_TOKEN`** over WIF (`infra-mcp-selftest`, grants only
     token self-lookup — see `platform-admin.tf`). This exists solely so the
     consumption-mirror Test can open a session and list tools; **real Vault access
     comes only from the project-plane Class C blueprint token**, never this one.
     Requires the infra tier applied (sets `PORTAL_MCP_JOB_VAULT_ROLE`); if unset the
     deploy is rejected with 400.

   For `hashicorp/vault-mcp-server` specifically: the image has **no `ENTRYPOINT`** and
   its default `CMD` hardcodes the `stdio` subcommand (`/bin/vault-mcp-server stdio`), so
   `TRANSPORT_MODE` env alone cannot flip it and passing just `http` as an argument fails
   with `exec: "http": executable file not found`. You must replace the whole command.
   Use: Transport **Streamable HTTP**, Listen port `8080`, **Arguments** (one per line)
   `/bin/vault-mcp-server` then `http`, Environment `TRANSPORT_HOST=0.0.0.0` /
   `TRANSPORT_PORT=8080`, and **tick Inject a Vault token**. `TRANSPORT_HOST=0.0.0.0` and
   `TRANSPORT_PORT` = Listen port are both required, or the health check never passes.

   Click **Deploy**. The portal renders a Docker Nomad job in `infra-mcp`, waits
   healthy, and the row appears with status **deployed**.
2. **Test** — click **Test** on the row. The portal registers a ContextForge peer →
   creates a **virtual server** + **scoped token** → `tools/list` through it returns
   **200**; the **same token on a decoy server returns 403** (scope isolation). The temp
   virtual-server + token are torn down; the deployed server keeps running. The **Test**
   column turns green: **passed (N tools)**.
3. **Publish** — click **Publish** (enabled only once the test passes; disabled once
   published). Writes the descriptor to `secret/infra/mcp-servers/<name>`; the server
   becomes discoverable by projects. **Delete** (danger) tears the Nomad job down.

   **Edit** (on the row) reopens the form prefilled with the current run config — use
   it to fix a bad image/args/env/port without re-typing everything (e.g. the
   `vault-mcp` arguments above). The name is the identity key and is locked. Saving
   re-registers the Nomad job **in place** (a version bump), and resets the server to
   **deployed** — re-run **Test** (and **Publish**) afterwards, since the run config changed.

**Pass:** status **deployed** → **Test** green `passed (N tools)` (200 own-server, 403
decoy isolation under the hood) → **Publish** flips status to **published** and writes
the descriptor.

### 3a. Verify the deployed server directly in ContextForge

The portal **Test** mirrors how a *project* consumes the server; you can also confirm the
deploy landed at the gateway itself. ContextForge runs as the `mcp-gateway` Nomad job in the
`infra` namespace, **plaintext HTTP on :4444**, reachable via the NLB locked to the operator
`/32` — get its base URL from `terraform output -raw mcp_gateway_addr` (`http://<nlb>:4444`).
`AUTH_REQUIRED=true` and Basic auth is disabled, so admin access is the Admin UI login or an
HS256 JWT signed with the gateway's `jwt_secret_key`. This is the same surface as the deeper
[Verify the MCP Gateway](./README.md#verify-the-mcp-gateway) walk in the infra README.

**UI.** Open `$(terraform output -raw mcp_gateway_addr)/admin` and sign in as **`admin@example.com`**
with the per-deploy bootstrap password from Vault:
```bash
vault kv get -field=admin_password secret/infra/mcp-gateway
```
Under **Gateways** (federated peers) the portal-registered peer named `<server>` appears;
**Tools** lists the tools it federated; **Virtual Servers** shows any composed during a test.
A reachable peer + non-empty tool list = the deploy reached the gateway.

**API.** Admin calls carry the HS256 JWT the gateway accepts (issuer `mcpgateway`, audience
`mcpgateway-api`, signed with `jwt_secret_key`). The simplest way to get one: log into the Admin
UI and copy the `Authorization: Bearer …` value from a request in browser devtools (the gateway
image's `mcpgateway.utils.create_jwt_token` utility mints the same token). Then:
```bash
MCP="$(terraform output -raw mcp_gateway_addr)"; JWT="<bearer-from-ui-or-utility>"   # http://<nlb>:4444 (operator /32)
curl -s "$MCP/gateways" -H "Authorization: Bearer $JWT" | jq '.[] | {name, reachable, url}'  # peer present + reachable
curl -s "$MCP/tools"    -H "Authorization: Bearer $JWT" | jq 'length'                         # tools federated (>0)
curl -s "$MCP/servers"  -H "Authorization: Bearer $JWT" | jq '.[].name'                       # virtual servers (if any)
```

**Pass:** the peer is listed and `reachable`, and `/tools` returns the server's tools (>0).

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

## 5. Enable project-admin self-service MCP deploy (R3 / B2)

The platform-admin **deploy** in §3 places a server in the shared `infra-mcp` namespace. R3/B2 adds a
*different* capability: a **project-admin** deploys a published, **blueprint-backed** MCP server into
**their own project's** Vault + Nomad namespace, with credentials brokered by a platform-authored
credential blueprint (never pasted). The §1 apply already gave the portal its `vault-provisioner`
workload identity (the row above); the per-namespace `portal-provisioner` role/policy it brokers against
is created by each project's `terraform/project` apply. These are the platform-admin steps that must happen **before** a project-admin
can deploy — do them in order, then hand off to
[`../project/PROJECT-ADMIN-RUNBOOK.md`](../project/PROJECT-ADMIN-RUNBOOK.md).

> The portal `/api/*` is OIDC-session-authed. Steps 1–4 are **control-plane operations with
> no dedicated admin screen** — the only platform-admin UI pages are *MCP servers* and *LLM
> models* (§3–§4) — so run them via the API, carrying your logged-in browser's session cookie
> (`-H "Cookie: portal_session=<value>"`). `$PORTAL` = `https://<nlb-dns>:8443`. Once they're
> done you **verify and manage** everything in the Portal UI — see §5a.

1. **Author + publish a credential blueprint.** Three seeds ship in the portal image (Class A
   `postgres-mcp`, B `generic-api-key`, C `vault-mcp`). To register one:
   ```bash
   # create-draft (body = the BlueprintManifest JSON, e.g. internal/blueprint/seeds/classA-postgres-mcp.json)
   curl -sk -X POST "$PORTAL/api/admin/blueprints"            -H 'Content-Type: application/json' -d @classA-postgres-mcp.json
   curl -sk -X POST "$PORTAL/api/admin/blueprints/postgres-mcp/1/validate"   # shape + policy-lint (static, no Vault)
   curl -sk -X POST "$PORTAL/api/admin/blueprints/postgres-mcp/1/publish"
   curl -sk "$PORTAL/api/admin/blueprints" | jq '.[] | {id,version,status,content_hash}'
   ```
   Note the published blueprint's `content_hash` — you bind it in the next step. Validate is now
   **static** — manifest shape + a render/lint of the generated policy against the allowed prefixes;
   it makes no Vault calls. Runtime correctness is proven by actually deploying the blueprint into a
   real project (step 2 onward). A `failed` result reports the offending `shape` or `policy-lint`
   check in the row detail. The manifest is read back from the Portal DB row (no Vault call), so the
   old `secret/data/infra/blueprints/*` 502 failure mode no longer applies.

2. **Bind the blueprint onto a published MCP server type.** This is what makes the type appear in a
   project-admin's deployable catalog (a server type with `status=published` **and** a non-nil
   `blueprint_ref`). The *MCP servers* screen's **Publish** button (§3) publishes a server *without*
   a blueprint, so binding a `blueprint_ref` is API-only:
   ```bash
   curl -sk -X POST "$PORTAL/api/admin/mcp-servers/postgres-mcp/publish" -H 'Content-Type: application/json' \
     -d '{"blueprint_ref":{"id":"postgres-mcp","version":1,"content_hash":"<hash-from-step-1>"}}'
   ```

3. **Bootstrap the first project-admin** for the project (RBAC Plan A). The grant is by **email** and
   stays **inert** until that user is actually in the project's `<project>-developers` Verify group
   (membership is re-checked at request time, so this is also how offboarding wins):
   ```bash
   curl -sk -X POST "$PORTAL/api/admin/projects/project-acme/roles" -H 'Content-Type: application/json' \
     -d '{"subject":"acme-admin@your.org","role":"project-admin"}'
   ```
   Project-admins then grant/revoke project-admin to other members of their own project (self-service,
   covered in the project-admin runbook).

4. **Confirm the provisioner brokering is live** (the portal's second workload identity comes from §1;
   the `portal-provisioner` role/policy it trades against are created per-namespace by each project's
   `terraform/project` apply). Verify them **in the target project namespace**:
   ```bash
   export VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true
   export VAULT_TOKEN="$(terraform output -raw vault_root_token)"
   vault policy read -namespace=project-acme portal-provisioner >/dev/null && echo "policy present"
   vault read -namespace=project-acme auth/jwt-nomad/role/portal-provisioner -format=json \
     | jq '{token_policies:.data.token_policies, token_ttl:.data.token_ttl, bound_claims:.data.bound_claims}'
   #   expect token_policies=["portal-provisioner"], token_ttl=300, and bound_claims pinning
   #   nomad_namespace=infra / nomad_job_id=developer-portal
   ```
   If the role/policy are missing, the project hasn't been applied yet — run its `terraform/project`
   apply first. If the portal itself was just restarted, it re-mints the `vault-provisioner` JWT over WIF
   on start: `nomad job restart -namespace infra -reschedule -on-error=fail developer-portal`.

### 5a. Verify & manage in the Portal UI

Sign in as the bootstrapped project-admin (a member of `<project>-developers`). The left nav now
shows two per-project entries — **`<project> · mcp servers`** and **`<project> · members`**:

- **Confirm the bind + grant worked** — open **`<project> · mcp servers`** → click **Deploy**. The
  **Server type** dropdown lists the blueprint-backed type (e.g. `postgres-mcp (…)`). If the dropdown
  is empty or **Deploy** is disabled, the type isn't published-with-blueprint (recheck steps 1–2) or
  the grant isn't live (steps 3–4). Each param the blueprint declares renders as a field; secret
  params render as masked password inputs.
- **Manage project-admins** — open **`<project> · members`** → **Grant project-admin to (email)** +
  **Grant**; **Revoke** per row. Grants stay inert until that user is in `<project>-developers`. The
  *first* admin is the API bootstrap in step 3; every subsequent grant/revoke is self-service here.

The project-admin's own deploy → test → delete walk lives in
[`../project/PROJECT-ADMIN-RUNBOOK.md`](../project/PROJECT-ADMIN-RUNBOOK.md).

**Pass:** a project-admin's catalog (UI **`<project> · mcp servers`** → **Deploy**, or
`GET /api/projects/project-acme/mcp-servers`) lists the blueprint-backed type under `deployable`, and
the deploy walk in [`../project/PROJECT-ADMIN-RUNBOOK.md`](../project/PROJECT-ADMIN-RUNBOOK.md)
succeeds end-to-end.

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
