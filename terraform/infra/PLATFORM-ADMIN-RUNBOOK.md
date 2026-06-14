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
| `developer-portal` job | redeploys with `PORTAL_MCP_GATEWAY_ADDR=http://127.0.0.1:4444`, `PORTAL_LLM_GATEWAY_ADDR=http://127.0.0.1:4000`, `PORTAL_MCP_NAMESPACE=infra-mcp`, `PORTAL_AGENT_NODE_POOL=agents` |

> If the key-bootstrap `local-exec` can't reach the gateway, the apply still
> succeeds; the portal logs a warning and disables only the admin plane. Manual
> fallback: create a `proxy_admin` key in the LiteLLM Admin UI and
> `vault kv patch secret/infra/llm-gateway portal_admin_key=<key>`.

---

## 2. Confirm the plane initialized

- Portal boot log: `admin plane: using in-memory control-plane store …` (or postgres,
  once `PORTAL_DB_DSN` is wired — see the (c) follow-up). A warning + disabled plane
  means a Vault read failed; check the `infra-platform-admin` policy attach.
- Identity / RBAC:
  ```bash
  curl -s https://<portal>/api/me        # platform-admin user → "roles":["platform-admin"]
  ```
  A **non**-`platform-admins` user hitting any `/api/admin/*` route must get **403**
  (`RequirePlatformAdmin`). Verify this explicitly.

---

## 3. MCP live verification (consumption-mirror)

Worked example: the HashiCorp Vault MCP server. In **Platform Admin → MCP Servers → Deploy**:

1. **Deploy** — image `hashicorp/vault-mcp-server` (+ run config from its docs),
   transport `streamable-http`, port/path. Portal renders a Docker Nomad job in
   `infra-mcp`, waits healthy.
2. **Test** — portal registers a ContextForge peer → creates a **virtual server** +
   **scoped token** → `tools/list` through it returns **200**; the **same token on a
   decoy server returns 403** (scope isolation). Temp virtual-server + token are torn
   down; the deployed server keeps running.
3. **Publish** — gated on `status=deployed` + test passed. Writes the descriptor to
   `secret/infra/mcp-servers/<name>`; the server becomes discoverable by projects.

**Pass:** 200 own-server call, 403 isolation, descriptor present after publish.

---

## 4. LLM live verification (consumption-mirror)

In **Platform Admin → LLM Models → Onboard** (provider key is written to
`secret/infra/llm-providers/<provider>` and injected at call time — never stored in
the control-plane DB):

1. **Onboard** — `/model/new` (provider key pulled from Vault at call time).
2. **Test** — portal mints a **scoped key** (`max_budget` + `rpm_limit`) →
   completion **200** → exceed `rpm_limit` → **429** → revoke key → **401**.
3. **Publish** — model joins the project allow-list.

**Pass:** 200 / 429 / 401 across the three scenarios; model published.

---

## 5. Regression gate (must stay green)

Confirm the `STORE_MODEL_IN_DB` flip and the portal redeploy didn't disturb existing
flows:

- Developer login → list projects → create / stop / SSH a workspace.
- MCP/LLM secrets present in the workspace; config-source models
  (`deepseek-v4-pro` / `deepseek-v4-flash`) still served; workspace Claude Code
  traffic unaffected.

---

## 6. Rollback

```bash
terraform apply -var enable_platform_admin=false
```

Drops the env/policy/namespace and reverts `STORE_MODEL_IN_DB`; the portal returns to
byte-identical developer-only behavior. Models already persisted in LiteLLM's Postgres
remain there but are no longer managed by the portal.

---

## After verification — (c) follow-up to make the store durable

The onboarding plane currently uses the in-memory control-plane store (the Nomad jobs,
gateway peers, and LiteLLM models persist in their own systems regardless). To make the
portal's own state durable, the **`store.Postgres`** impl is already built and verified
and is selected automatically when `PORTAL_DB_DSN` is set. The remaining infra is a
`portal-postgres` Nomad job + a Vault DB dynamic role (`portal-app`) that renders the
DSN into the portal job env — to be added next.
