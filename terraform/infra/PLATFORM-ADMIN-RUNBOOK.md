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
  deploying the A/B/C reference blueprints into a real project. The portal needs read on the
  manifest KV path (`secret/data/infra/blueprints/*`, granted by `infra-platform-admin`) so the
  lint can read the stored manifest back.
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
| `vault_policy.portal_provisioning` | creates the **`portal-blueprint-provisioning`** policy and appends it to the portal's root WIF role (`infra-developer-portal`), so a **project-admin** can instantiate a credential blueprint into their project's Vault namespace. The portal's Executor scopes each Vault call to a child namespace via `WithNamespace`. **Confirmed limitation:** a root-token request to a child namespace is ACL-matched against the **namespace-prefixed** path (`<child>/…`); both a bare relative grant **and** the `+/` wildcard fail to match the namespace segment (only an explicit `<child>/…` prefix matches), so this policy **403s on a real deploy** as written. The project-deploy plane needs the **§5 hardening** first — per-project policy attachment or a dedicated per-namespace provisioner token. Containment is app-level (always-target-a-namespace + generated-policy lint) plus explicit `deny` stanzas — see `portal-provisioning-policy.hcl` / `terraform/infra/README.md`. **Enables the project-admin deploy plane (§5), pending the cross-namespace hardening.** |

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
   - **Listen port** — the port the server listens on in the container.
   - **MCP path** (optional) — defaults to `/sse` or `/mcp` by transport.
   - **Arguments** (optional, one per line) and **Environment** (optional, `KEY=VALUE`
     per line — **non-secret values only**; secrets are injected from Vault).

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

## 5. Enable project-admin self-service MCP deploy (R3 / B2)

The platform-admin **deploy** in §3 places a server in the shared `infra-mcp` namespace. R3/B2 adds a
*different* capability: a **project-admin** deploys a published, **blueprint-backed** MCP server into
**their own project's** Vault + Nomad namespace, with credentials brokered by a platform-authored
credential blueprint (never pasted). The §1 apply already attached the `portal-blueprint-provisioning`
policy (the row above). These are the platform-admin steps that must happen **before** a project-admin
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
   check in the row detail. If validate returns **502 “upstream service error”** with a
   `read … secret/data/infra/blueprints/… 403`, the portal's WIF role is missing the manifest-read
   grant (`secret/data/infra/blueprints/*` in `infra-platform-admin`) — re-run the §1 apply.

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

4. **Confirm the provisioning grant is live** (it was applied in §1; verify the portal actually carries
   it after the restart):
   ```bash
   export VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true
   export VAULT_TOKEN="$(terraform output -raw vault_root_token)"
   vault policy read portal-blueprint-provisioning >/dev/null && echo "policy present"
   vault read auth/jwt-nomad/role/infra-developer-portal -format=json | jq '.data.token_policies'
   #   expect the list to include "portal-blueprint-provisioning"
   ```
   If it's missing from the running portal's token, restart so it re-authenticates over WIF:
   `nomad job restart -namespace infra -reschedule -on-error=fail developer-portal`.

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
byte-identical developer-only behavior. This also drops `vault_policy.portal_provisioning`
and detaches `portal-blueprint-provisioning` from the portal WIF role, so the
**project-admin deploy plane (§5) becomes unavailable** — already-deployed project MCP
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
