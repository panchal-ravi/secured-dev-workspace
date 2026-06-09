# Developer Portal (`portal/`)

A self-service web app where a developer logs in with **IBM Verify SaaS (OIDC)**, sees the
**projects their IBM Verify groups grant**, and **creates, lists, and manages (stop / start /
delete) dev workspaces** — each workspace card carries copy-paste **VSCode Remote-SSH** instructions
and an info section of the flavor's baked-in features (Claude Code → DeepSeek, a read-only DB MCP
reached through the central ContextForge gateway, a short-lived GitHub App push token — no static
PAT).

It is the **direct-API equivalent of the `terraform/workspace` tier**: a Go backend calls Nomad,
Boundary, and Vault to build the same per-workspace resource graph (dynamic host volume + job,
SSH target with injected Vault-signed certs, per-developer managed group / role / alias).

> **Status: PoC, production-hardened.** Runs locally against the live HashiStack and ships as
> a containerized, Vault-WIF Nomad job behind a new NLB `:8443` listener
> (see [Production deploy](#production-deploy-nomad--vault-wif)). The backend has structured
> JSON logging with request ids, panic recovery, typed errors (no internal detail leaks to
> clients), security headers, same-origin CSRF defense, per-user rate limiting, configurable
> TLS, graceful shutdown, and `/health` + `/readyz` probes. Remaining items are in [Roadmap](#roadmap).

## Two ways to run it

The portal can run in **either** of two modes — pick one. They reach the same HashiStack and use
the same IBM Verify OIDC app concept; they differ only in *how the process runs* and *where its
secrets come from*.

| | **Mode A — local machine** | **Mode B — Nomad job** |
|---|---|---|
| Use for | the PoC / local development | production |
| Process | `go run` on your laptop | containerized job in the `infra` namespace |
| Secrets | a gitignored `.env` (from `terraform output`) | Vault KV read over **WIF** (no static token) |
| Endpoint | `http://localhost:8080` | NLB `:8443`, in-process TLS, operator /32 |
| Verify redirect URI | `http://localhost:8080/auth/callback` | `https://<nlb-dns>:8443/auth/callback` |
| Follow | **Steps 1–3 below** | [Production deploy](#production-deploy-nomad--vault-wif) |

> **Mode A (local)** is the quickest way to try the portal — it's Steps 1–3 in this README.
> **Mode B (Nomad)** is the self-contained production deploy in its own section at the end.

## Architecture

```
Carbon React SPA  ──►  Go backend (trusted)  ──►  Vault   (read project descriptors + job templates)
  (OIDC login)            authz by groups     ──►  Nomad   (host volume + parse/register job)
                                              ──►  Boundary(host/target/managed-group/role/alias)
```

- **Login**: the portal's own IBM Verify app (Authorization Code + PKCE). It reads `email` +
  `groups` from the ID token. This is separate from the *SSH-time* Boundary login the developer
  runs when connecting.
- **Authorization**: a project is visible only if its `developers_group_name` is in the
  developer's `groups`.
- **Discovery**: the project tier publishes a per-project descriptor to Vault KV
  (`terraform/project/portal.tf` → `secret/projects/<project>/portal-descriptor`); the portal
  lists and reads these.

## Layout

```
backend/   Go module (cmd/portal + internal/{config,auth,hashistack,descriptor,portgen,jobrender,workspace,api})
frontend/  Vite + React + @carbon/react  (build output → backend/web, served by the binary)
helper/    macOS secured-ws:// helper — runs the Boundary login + manages ~/.ssh/config (the Connect flow)
```

## Prerequisites

1. The platform + project tiers are applied and **Vault is unsealed**. The project tier owns the
   repo every workspace clones, so set `workspace_git_repo_url` in `<project>.tfvars`, then apply
   the project tier (it renders the project-static values into each job template and publishes the
   descriptor the portal reads):
   ```bash
   cd terraform/project && terraform apply -var-file=<project>.tfvars   # your reviewed apply
   ```
2. Go ≥ 1.24 and Node ≥ 20 on the machine running the portal.
3. Network egress from the portal host to the NLB ports (Boundary 9200, Nomad 4646, Vault 8200)
   and to IBM Verify. For the local PoC your caller IP must be in the NLB security group's /32.

## Step 1 — register the portal's IBM Verify OIDC app (manual, one-time)

In the IBM Verify console create an OIDC application (this mirrors the Boundary/Nomad apps in
`terraform/infra/modules/identity/verify.tf`):

- Grant type: **Authorization Code** (PKCE).
- Redirect URI: `http://localhost:8080/auth/callback`.
- Scopes: `openid`, `email`, `groups`, `profile`.
- Group attribute mapping so the `groups` claim is populated (lowercased), matching the rest of
  the stack.
- Entitle the relevant users/groups.

Note the **issuer**, **client id**, and **client secret**. The **issuer** is the full IBM Verify
OIDC endpoint, *not* the bare tenant host: `https://<tenant>.verify.ibm.com/oidc/endpoint/default`
(same value Boundary/Nomad use in `terraform/infra/modules/identity`). The backend's go-oidc client
runs discovery at `<issuer>/.well-known/openid-configuration` and rejects any mismatch, so a bare
host fails at startup with `auth: discover issuer`. Verify with:
```bash
curl -s https://<tenant>.verify.ibm.com/oidc/endpoint/default/.well-known/openid-configuration | jq .issuer
```
(Moving this into the `identity` Terraform module is a [roadmap](#roadmap) item.)

## Step 2 — configuration

The backend reads everything from the environment. Source the HashiStack values straight from the
Terraform outputs into a **gitignored** env file (never echo secrets to the terminal):

```bash
cd terraform/infra
cat > ../../portal/backend/.env <<EOF
PORTAL_OIDC_ISSUER=https://<tenant>.verify.ibm.com/oidc/endpoint/default
PORTAL_OIDC_CLIENT_ID=<client id from Step 1>
PORTAL_OIDC_CLIENT_SECRET=<client secret from Step 1>
PORTAL_OIDC_REDIRECT_URL=http://localhost:8080/auth/callback
PORTAL_SESSION_SECRET=$(openssl rand -hex 32)

PORTAL_BOUNDARY_ADDR=$(terraform output -raw boundary_addr)
PORTAL_BOUNDARY_AUTH_METHOD_ID=$(terraform output -raw admin_auth_method_id)
PORTAL_BOUNDARY_LOGIN=$(terraform output -raw admin_login_name)
PORTAL_BOUNDARY_PASSWORD=$(terraform output -raw admin_password)

PORTAL_NOMAD_ADDR=$(terraform output -raw nomad_addr)
PORTAL_NOMAD_TOKEN=$(terraform output -raw nomad_management_token)
PORTAL_VAULT_ADDR=$(terraform output -raw vault_addr)
PORTAL_VAULT_TOKEN=$(terraform output -raw vault_root_token)
PORTAL_VAULT_KV_MOUNT=$(terraform output -raw kv_mount_path)
EOF
```

| Variable | Source | Notes |
|---|---|---|
| `PORTAL_LISTEN_ADDR` | optional | default `:8080` |
| `PORTAL_OIDC_*` | Step 1 | the portal's Verify app |
| `PORTAL_SESSION_SECRET` | generate | cookie signing key |
| `PORTAL_BOUNDARY_*` | infra outputs | admin password auth (PoC) |
| `PORTAL_NOMAD_*`, `PORTAL_VAULT_*` | infra outputs | mgmt token / root token (PoC) |
| `PORTAL_SSH_PORT_RANGE` | optional | `min-max`, default `2222-2399` |
| `PORTAL_LOG_LEVEL` | optional | `debug\|info\|warn\|error`, default `info` |
| `PORTAL_SECURE_COOKIES` | optional | default inferred from the redirect-URL scheme |
| `PORTAL_TLS_CERT_FILE`, `PORTAL_TLS_KEY_FILE` | optional | set both to serve HTTPS in-process (production `:8443`) |
| `PORTAL_HASHISTACK_CA_CERT`, `PORTAL_TLS_SKIP_VERIFY` | optional | verify HashiStack TLS against a CA; skip-verify defaults `true` (PoC) |
| `PORTAL_VAULT_TOKEN_FILE` | optional | WIF: re-read the Vault token from a Nomad-managed file (overrides `PORTAL_VAULT_TOKEN`) |
| `PORTAL_RATE_LIMIT_RPS`, `PORTAL_RATE_LIMIT_BURST` | optional | per-user limit on mutating endpoints (default `5`/`10`, `0` disables) |
| `PORTAL_SHUTDOWN_TIMEOUT` | optional | graceful-drain seconds on SIGTERM, default `15` |

For the local PoC, HashiStack TLS is self-signed and the clients skip verification (like the
CLIs); production sets `PORTAL_TLS_SKIP_VERIFY=false` with `PORTAL_HASHISTACK_CA_CERT`, or — as
the Nomad deploy does — reaches the stack over loopback where skip-verify is acceptable.

## Step 3 — build & run on your local machine (Mode A)

```bash
# 1. Build the SPA into backend/web
cd portal/frontend && npm install && npm run build

# 2. Run the backend (serves the SPA + API on :8080)
cd ../backend
set -a; . ./.env; set +a      # load the env file
go run ./cmd/portal
```

Open <http://localhost:8080>, sign in with IBM Verify, open a project, and create a workspace.
Because the backend is remote, connecting is driven by a small **local helper** (the macOS
`secured-ws://` app in [`helper/`](helper/); a first-run install is offered in the UI): click
**Authenticate** in the project's *Connect to your workspaces* panel to run the
`boundary authenticate oidc` SSO login once per terminal session, then **Open** on a workspace card
writes the `~/.ssh/config` block and launches your IDE (VSCode Remote-SSH). Each card also exposes
the **ProxyCommand** block to copy manually, plus a transparent-session (Boundary Client Agent)
method.

## Verifying a workspace (Git, Claude Code, MCP, LLM gateway, GPU)

After you connect with VSCode Remote-SSH you land in the workspace as **`dev`**. Open a terminal
there and run the checks below — each confirms one feature baked into the workspace job template
(`terraform/project/templates/{dev,gpu}-workspace.nomad.hcl`). None of them print a secret.

### 0. Workspace is up

```bash
whoami                 # dev
ls ~/project           # the project repo, cloned into /home/dev/project on first boot
```

### 1. Git — pre-set identity + Vault-minted GitHub App push token

Git is pre-configured for the logged-in developer, and the push credential is a **short-lived
GitHub App installation token** (1h, auto-refreshed by Vault into the `/secrets` tmpfs) served
through a credential helper — there is **no static PAT and no key on `/home/dev`**.

```bash
git config --global --list | grep '^user\.'                 # your email + name, pre-set (no manual git config)
cd ~/project && git remote -v && git log --oneline -1        # repo cloned; origin = the project repo
git config --global credential.https://github.com.helper     # → /local/git-credential-helper (serves the token)
git ls-remote origin >/dev/null 2>&1 && echo "github auth OK" # exercises the helper against github.com
```

To prove **write** access (the token is scoped `contents:write`), make a trivial commit and push:
```bash
cd ~/project && git commit --allow-empty -m "workspace push test" && git push
```

### 2. Claude Code — installed and governed

```bash
claude --version
cat /etc/claude-code/managed-settings.json   # enforced settings, OUTSIDE /home/dev so the dev can't override them
claude -p "Reply with exactly: workspace-ok"
```

### 3. Read-only database via MCP (`demo-db`, central ContextForge gateway)

The entrypoint registers the project's **remote** virtual MCP server (`demo-db`) for the `dev` user
over SSE — its URL + per-project bearer token are rendered to `/secrets` over WIF (never on
`/home/dev`). Registration is idempotent and non-fatal (a missing MCP tool must never cost you the
SSH session).

```bash
claude mcp list                       # demo-db present, transport sse, status connected
claude mcp get demo-db                # URL = the gateway; the bearer token is NOT printed
# Exercise it end-to-end (read-only):
claude -p "Use the demo-db MCP server to list the tables, then show 3 rows from one. It is read-only."
```
If `demo-db` is missing, check `/secrets/mcp-url` and `/secrets/mcp-token` rendered and that the
gateway is reachable from the node.

### 4. Governed LLM gateway (LiteLLM → DeepSeek)

Claude Code is pointed at the shared **LiteLLM gateway**, not the public Anthropic API: the managed
settings set `ANTHROPIC_BASE_URL` to the node-private gateway, and an `apiKeyHelper`
(`/usr/local/bin/llm-key`) serves the project's **scoped virtual key** (allowed models + budget +
rpm) from `/secrets/llm-key` at call time. Models are remapped to `deepseek-v4-pro` (opus/sonnet)
and `deepseek-v4-flash` (haiku/subagent).

```bash
# a. Config is enforced + the key helper works
cat /etc/claude-code/managed-settings.json           # ANTHROPIC_BASE_URL → gateway, not api.anthropic.com
/usr/local/bin/llm-key | head -c 12; echo …           # helper prints the virtual key (don't paste it)

# b. Raw round-trip — proves reachability + key + Anthropic→DeepSeek translation
BASE=$(jq -r .env.ANTHROPIC_BASE_URL /etc/claude-code/managed-settings.json)
KEY=$(/usr/local/bin/llm-key)
curl -sS -w '\nHTTP %{http_code}\n' "$BASE/v1/messages" \
  -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" -H "content-type: application/json" \
  -d '{"model":"deepseek-v4-pro","max_tokens":64,"messages":[{"role":"user","content":"reply with the single word: pong"}]}'
# expect HTTP 200 + an Anthropic-shaped message containing "pong"

# c. Scope is enforced — a model the key is NOT allowed is rejected
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' "$BASE/v1/messages" \
  -H "x-api-key: $KEY" -H "anthropic-version: 2023-06-01" -H "content-type: application/json" \
  -d '{"model":"gpt-4o","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}'
# expect 400/401 (model not in the key's allowlist)

# d. End-to-end through Claude Code itself
claude -p "Reply with exactly: gateway-ok"
```

To confirm the call was actually **metered by the gateway** (not leaking to the public API), check the
key's spend from an operator machine (the admin URL `<nlb>:4000` is locked to the operator /32):

```bash
curl -sS "https://<nlb-dns>:4000/key/info" \
  -H "Authorization: Bearer $MASTER_KEY" -G --data-urlencode "key=$KEY" | jq '.info.spend, .info.models'
# spend increments after tests b–d; models = the project's allowlist
```

### 5. GPU (gpu-workspace flavor only)

Workspaces created from the **gpu-workspace** flavor schedule on the `gpu` Nomad node pool and get an
NVIDIA **T4** injected (the task runs with `runtime = "nvidia"` and a `device "nvidia/gpu"` request),
so the driver + CUDA are visible inside the container:

```bash
nvidia-smi             # the T4 shows up (name, memory, driver/CUDA version)
ls /dev/nvidia*        # nvidia0 / nvidiactl present
```
If `nvidia-smi` is absent you're on a **CPU flavor** (a non-GPU workspace has no `/dev/nvidia*`), or
the GPU node is off — it's gated by `enable_gpu_node` in `terraform/infra/terraform.tfvars`.

## Tests

```bash
cd portal/backend && go test ./...    # portgen, jobrender, descriptor authz, identity, sshconfig
```

## Production deploy (Nomad + Vault WIF)

**(Mode B — the alternative to running locally.)** The portal ships as a containerized Nomad job
in the `infra` namespace, behind a new NLB `:8443` listener locked to the operator /32 — the direct
analog of the MCP gateway deploy. Unlike Mode A there is no `.env`: the job authenticates to Vault
over WIF and reads its secrets from KV.

1. **Build & push the image** (context is `portal/`, builds the SPA + backend into one image):
   ```bash
   ./scripts/build-image.sh           # → panchalravi/developer-portal:poc, pushed
   ```
2. **Register the Verify app redirect URI** for the NLB host: `https://<nlb-dns>:8443/auth/callback`
   (the `developer_portal_addr` output prints the base URL after apply).
3. **Apply the infra tier** with the portal's OIDC app details:
   ```bash
   cd terraform/infra
   terraform apply \
     -var 'portal_oidc_issuer=https://<tenant>.verify.ibm.com/oidc/endpoint/default' \
     -var 'portal_oidc_client_id=<client id>' \
     -var 'portal_oidc_client_secret=<client secret>'   # your reviewed apply
   ```

`terraform/infra/developer-portal.tf` generates the session secret + a self-signed TLS cert,
stores them with the Boundary admin / Nomad mgmt creds in Vault KV `infra/developer-portal`,
grants a **WIF** role/policy scoped to that path plus the project descriptors, and runs the
job (`templates/developer-portal.nomad.hcl.tftpl`). The job authenticates to Vault over WIF
(no static token), terminates its own TLS on `:8443` (so `Secure` cookies are honored behind
the TCP-passthrough NLB), and reaches Vault/Nomad/Boundary over loopback. Moving the Verify
app into `terraform/infra/modules/identity` is the remaining identity-as-code step.

## Security notes

- The portal is a **trusted privileged backend**: it holds Boundary admin / Nomad mgmt / Vault
  access and enforces per-project access in app code from the OIDC `groups` claim. In production
  it authenticates to Vault via **WIF** (no static token) and reads the privileged creds from
  Vault KV; only run it where those credentials are safe.
- Secrets are read from the environment/files and never logged or placed on command lines.
  Upstream errors are logged server-side with a request id; clients get only a generic message
  plus that id, so backend detail never leaks.
- **Defenses**: security headers + CSP on every response, same-origin CSRF check on mutating
  requests (layered on `SameSite=Lax`), per-user rate limiting, request-size limits, panic
  recovery, and in-process TLS termination for `Secure` cookies.
- The developer never holds an SSH key — Boundary's worker injects a short-lived Vault-signed
  cert per session, exactly as in the Terraform path.

## Roadmap

- **Identity-as-code**: move the portal's Verify app into `terraform/infra/modules/identity`
  (still registered manually today).
- **Horizontal scale-out**: the deploy is a hardened *single* instance. Running N replicas
  needs a shared store for port + workspace-name allocation (today an in-process mutex closes
  the in-instance race) and a shared rate-limit store.
- **Project Team owner persona**: portal flows to *create* projects (namespace, Boundary scope,
  Vault paths) that write the same descriptor this portal already reads.
- **Workspace lifecycle**: stop / start / delete and log tailing ship; continuous status polling
  (today only while a workspace is still starting) and quotas remain.
- **TLS upgrade**: the `:8443` cert is self-signed; issue it from Vault PKI.
- **Multiple repos**: today the project pins one repo (`workspace_git_repo_url`) baked into the
  template; let a project publish several and have the developer pick one at create time.
```
