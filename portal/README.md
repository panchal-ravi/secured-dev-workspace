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

> **Status: PoC.** Runs locally against the live HashiStack. Production hardening and an
> NLB-hosted deployment are tracked in [Roadmap](#roadmap).

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

All HashiStack TLS is self-signed; the clients skip verification, like the CLIs.

## Step 3 — build and run

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

## Tests

```bash
cd portal/backend && go test ./...    # portgen, jobrender, descriptor authz, identity, sshconfig
```

## Security notes (PoC)

- The portal is a **trusted privileged backend**: it holds Boundary admin / Nomad mgmt / Vault
  tokens and enforces per-project access in app code from the OIDC `groups` claim. Run it only
  where those credentials are safe.
- Secrets are read from the environment and never logged or placed on command lines.
- The developer never holds an SSH key — Boundary's worker injects a short-lived Vault-signed
  cert per session, exactly as in the Terraform path.

## Roadmap

- **Production deploy**: package as a Nomad job behind a new NLB `:8443` listener (/32-restricted);
  move the portal's Verify app into `terraform/infra/modules/identity`; authenticate to Vault via
  **WIF** and read short-lived Nomad/Boundary creds from Vault instead of static admin tokens.
- **Project Team owner persona**: portal flows to *create* projects (namespace, Boundary scope,
  Vault paths) that write the same descriptor this portal already reads.
- **Workspace lifecycle**: stop / start / **delete** and log tailing now ship (per-workspace,
  owner-scoped); continuous status polling (today only while a workspace is still starting) and
  quotas remain.
- **Hardening**: persistence, concurrency/locking, port-allocation race safety, audit logging,
  full error states.
- **Multiple repos**: today the project pins one repo (`workspace_git_repo_url`) baked into the
  template; let a project publish several and have the developer pick one at create time.
```
