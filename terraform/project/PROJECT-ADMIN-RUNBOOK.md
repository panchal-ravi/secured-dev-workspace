# Project-Admin — MCP Deploy & Role Management Runbook

Operator/persona runbook for a **project-admin**: deploy a published, blueprint-backed MCP server into
**your own project**, verify it, tear it down lease-safely, and manage who else is a project-admin.

**Who is a project-admin?** A project *member* — a user already in the project's `<project>-developers`
IBM Verify group — who has been **elevated to `project-admin` in the Portal DB**. There is no Verify
group per role: membership stays IdP-owned (so removing someone from `<project>-developers` instantly
voids all their access, including admin), while *role elevation* is self-service in the portal. A
platform-admin bootstraps the first project-admin; project-admins grant/revoke the rest within their own
project.

**What a project-admin can do (gated by `RequireProjectRole(project-admin)` = live membership **and** a
Portal-DB grant):** deploy/test/delete blueprint-backed MCP servers in their project, and grant/revoke
project-admin to other members. Credentials are **blueprint-provisioned, never pasted** — the project's
Vault namespace brokers them.

> **Auth:** the portal `/api/*` is OIDC-session-authed. Drive everything below from the **portal UI**
> (recommended), or with `curl` carrying your logged-in browser's session cookie. Throughout:
> ```bash
> export PORTAL="https://<nlb-dns>:8443"          # terraform output nlb_dns_name, port 8443
> export COOKIE="portal_session=<from-your-browser-devtools>"
> CURL() { curl -sk -H "Cookie: $COOKIE" "$@"; }   # -k: portal serves a self-signed cert
> export PROJECT="project-acme"                     # your project (== Vault ns == Nomad ns)
> ```

---

## 0. Preconditions (platform-admin / operator — verify these exist first)

All of these are set up *before* you deploy; most are not your action. If a deploy 404s or 403s, check
here first.

- **The project tier is applied** — `terraform/project/` created the project's Vault namespace
  (`vault_namespace.project`, path = project name), its per-namespace `jwt-nomad` WIF backend, and the
  Nomad namespace. See [`README.md`](./README.md).
- **`demo-db` is reachable from the `agents` node pool** (the Class A `postgres-mcp` blueprint's
  `rotate-root` connects to it at apply/deploy time).
- **A blueprint-backed server type is published** — a platform-admin authored + published the blueprint
  and bound it onto a published MCP server type. See the platform-admin runbook
  [`../infra/PLATFORM-ADMIN-RUNBOOK.md` §5](../infra/PLATFORM-ADMIN-RUNBOOK.md).
- **Your project-admin grant is bootstrapped** — a platform-admin ran
  `POST /api/admin/projects/<project>/roles {subject:"you@org", role:"project-admin"}`, and **you are in
  the `<project>-developers` Verify group** (the grant is inert otherwise).
- **The portal provisioning grant is live** — `enable_platform_admin=true` applied and the portal
  restarted, so `portal-blueprint-provisioning` is on its WIF role (platform-admin runbook §5.4).

---

## 1. Confirm your role

```bash
CURL "$PORTAL/api/me" | jq '{email, project_roles}'
# expect project_roles to include: {"project":"project-acme","role":"project-admin"}
```

In the UI, the project's **MCP servers** entry appears in the nav (gated on `project_roles`). If
`project_roles` is empty: either the grant wasn't bootstrapped, or you're not in `<project>-developers`
(membership is re-checked on every request — fix the group, no re-grant needed).

---

## 2. Deploy → test → delete (the lifecycle)

Drive this from **Project Admin → <project> → MCP servers**, or with the calls below.

### 2a. List the catalog

```bash
CURL "$PORTAL/api/projects/$PROJECT/mcp-servers" | jq
# deployable[]: published blueprint-backed types you can deploy; deployed[]: already running in your project
```

### 2b. Deploy

The Class A `postgres-mcp` blueprint requires **all six** params (the seed prompts come from
`internal/blueprint/seeds/classA-postgres-mcp.json`). `bootstrap_password` is a **secret** — it's
write-only: used once to configure + immediately rotate the DB root, never persisted, logged, or echoed.

| param | value for `demo-db` |
|---|---|
| `connection_url` | `postgresql://{{username}}:{{password}}@<node-private-ip>:15432/appdb?sslmode=disable` (keep the `{{username}}/{{password}}` templating — Vault fills it) |
| `bootstrap_username` | `vaultadmin` (the demo-db superuser Vault rotates) |
| `bootstrap_password` | the `vaultadmin` password (secret) |
| `db_host` | `<node-private-ip>` (`terraform output instance_private_ip` in the infra tier) |
| `db_port` | `15432` |
| `db_name` | `appdb` |

```bash
CURL -X POST "$PORTAL/api/projects/$PROJECT/mcp-servers" -H 'Content-Type: application/json' -d '{
  "server_type":"postgres-mcp",
  "params":{
    "connection_url":"postgresql://{{username}}:{{password}}@<node-private-ip>:15432/appdb?sslmode=disable",
    "bootstrap_username":"vaultadmin",
    "bootstrap_password":"<vaultadmin-password>",
    "db_host":"<node-private-ip>",
    "db_port":"15432",
    "db_name":"appdb"
  }
}' | jq
# expect HTTP 201, status "deployed", a job_id, a gateway_url, and a non-empty instance blob.
```

If anything fails *after* the blueprint is instantiated, the deploy rolls back automatically (Vault
state deprovisioned, Nomad job purged, no row persisted) — you can safely retry.

### 2c. Test (consumption-mirror)

```bash
CURL -X POST "$PORTAL/api/projects/$PROJECT/mcp-servers/postgres-mcp/test" | jq '.test_result'
# expect: passed:true, tools_discovered>0, own_server_ok:true, admin_denied:true, other_server_denied:true
```

This registers a ContextForge peer, composes a temporary virtual server + scoped token, confirms the
token reaches **its own** server (200) but is denied **admin** and a **decoy** server (403), then tears
down the temp artifacts (the peer stays).

### 2d. Delete (lease-safe teardown)

```bash
CURL -X DELETE "$PORTAL/api/projects/$PROJECT/mcp-servers/postgres-mcp" -o /dev/null -w '%{http_code}\n'
# expect 204
```

Purges the Nomad job, deregisters the peer, then deprovisions the blueprint instance — **dynamic leases
are revoked BEFORE the engine is unmounted** — and drops the row.

---

## 3. Verify on the platform (what the API can't show you)

Run between **deploy** and **delete**, scoped to your project's Vault namespace. (Needs operator Vault
access — coordinate with the platform-admin if you don't have a token.)

```bash
export VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true   # from terraform/infra
export VAULT_TOKEN="<a token valid in the project namespace>"
export VAULT_NAMESPACE="$PROJECT"

vault secrets list | grep "${PROJECT}-pg"                 # engine mounted at database/<project>-pg
vault read "database/${PROJECT}-pg/creds/mcp-ro"          # dynamic SELECT-only creds actually mint (proves rotate-root + role)
vault read auth/jwt-nomad/role/mcp-postgres-mcp           # the WIF role the Nomad job assumes
nomad job status -namespace "$PROJECT" "mcp-${PROJECT}-postgres-mcp"   # running in the PROJECT Nomad namespace
```

**After delete**, confirm clean teardown:

```bash
vault secrets list | grep "${PROJECT}-pg"                 # → gone (engine unmounted)
nomad job status -namespace "$PROJECT" "mcp-${PROJECT}-postgres-mcp"   # → not found (purged)
CURL "$PORTAL/api/projects/$PROJECT/mcp-servers" | jq '.deployed'      # → []
```

---

## 4. Self-service role management

As a project-admin you grant/revoke **project-admin** to other members of *your* project (UI: the
project's Members/Roles screen, or the calls below). Grants are by email and inert until that user is in
`<project>-developers`.

```bash
# grant another member
CURL -X POST "$PORTAL/api/projects/$PROJECT/roles" -H 'Content-Type: application/json' \
  -d '{"subject":"teammate@your.org","role":"project-admin"}'

# revoke
CURL -X DELETE "$PORTAL/api/projects/$PROJECT/roles/teammate@your.org" -o /dev/null -w '%{http_code}\n'
```

- **Last-admin guard:** a revoke that would leave the project with **zero** project-admins is refused
  (avoids lockout; a platform-admin is the recovery path via the bootstrap route).
- **Offboarding wins:** removing a user from `<project>-developers` immediately voids their
  project-admin — any subsequent request → **403**, no revoke needed.

---

## 5. RBAC negative checks & pass criteria

- A project **developer** (in `<project>-developers` but without the project-admin grant) → **403** on
  every `/api/projects/<project>/mcp-servers` route.
- A user with a **stale grant** who is **not** in `<project>-developers` → **403** (membership wins).
- **Pass = the full credential-brokered, lease-safe lifecycle:** deploy `201` + instance blob → Vault
  engine mounted + creds minting + WIF role + job running in the project namespace → test `passed:true`
  with 200/403 isolation → delete `204` → engine unmounted, job purged, no leftover leases, catalog
  `deployed:[]`.

---

## 6. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Deploy **403** | Your token predates the policy attach → ask the operator to restart `developer-portal`; or you lack the grant / aren't in `<project>-developers`. |
| Deploy **404** (server type) | The type isn't published or isn't blueprint-bound — platform-admin must complete [PLATFORM-ADMIN-RUNBOOK §5](../infra/PLATFORM-ADMIN-RUNBOOK.md) steps 1–2. |
| Deploy **409** | That server type is already deployed in this project (one instance per type per project) — delete it first. |
| Deploy succeeds but job unhealthy / Class A error | `demo-db` not reachable from the `agents` node pool, or wrong `db_host`/`db_port` (`15432`)/`bootstrap_*` creds. |
| Test fails (`passed:false`) | Tools didn't discover (>0 required) or the 200/403 isolation didn't hold — check the ContextForge gateway and the deployed server's health. |
| Portal logs | `nomad alloc logs -namespace infra -job developer-portal` (the plane is on the `infra` namespace portal job). |

---

See also: [`README.md`](./README.md) (project tier), the platform-admin enablement steps in
[`../infra/PLATFORM-ADMIN-RUNBOOK.md`](../infra/PLATFORM-ADMIN-RUNBOOK.md), and the package overview in
`portal/backend/internal/projectadmin/README.md`.
