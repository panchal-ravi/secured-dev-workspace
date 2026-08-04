# End-to-end walkthrough — all roles

Drives the three user roles through the Portal UI against a running platform:
**platform-admin** (§1), **project-admin** (§2), **project-user / developer** (§3), plus the
AI-agents plane (§4) and project teardown (§5).

The core path is §1 → §2 → §3.1–§3.3 → §5. Sections marked *(optional)* — §3.4–§3.7 and §4 — are
independent deep-dives; skipping them does not affect anything that follows.

Stand the platform up first with [`README.md`](./README.md). Everything here is **portal-driven** —
no Terraform, no Nomad or Vault access. Each step is verified in the UI; the deeper backend proofs
(Vault paths, Nomad jobs, Boundary scopes, EFS access points) are collected in
**[Appendix A](#appendix-a--operator-backend-verification)**, which needs operator credentials no
role holds.

## Before you start

You need the **Portal URL** — `terraform output -raw developer_portal_addr`, or ask your platform
operator. It uses a self-signed cert, so accept the browser warning.

One-time setup, outside the Portal:

1. **IBM Verify groups**
   - `platform-admins` — must contain the platform-admin.
   - `<project>-developers` (e.g. `project-acme-developers`) — must contain the first project-admin
     **and** every developer. Portal role grants stay inert until the user is in this group;
     membership is re-checked on every request.
2. **GitHub App credentials** for §2.1: App ID, Installation ID, a rotated private-key PEM, and the
   repo list.
3. **Boundary CLI** on the developer's laptop (`brew install hashicorp/tap/boundary`).
4. **Network access** — every endpoint is locked to the `/32` that ran `terraform apply`. From a
   different egress IP you will not reach the Portal; see README §1.

This walkthrough assumes the platform was applied with `enable_platform_admin`,
`enable_developer_portal`, `enable_agent_nodes` and `enable_shared_volume` all `true`.

---

## 1. Platform-admin

Sign in to the Portal via IBM Verify as a `platform-admins` member. The admin nav shows
**Projects (admin)**, **LLM models**, **Base templates**, **Coding agents**.

### 1.1 Onboard an LLM model

**LLM models** page (`/admin/llm-models`):

1. **Provider key** — set the provider (e.g. `deepseek`) API key. Write-only; stored in Vault, never
   echoed back.
2. **Onboard model** — name (e.g. `deepseek-v4-pro`), provider, backend model
   (`deepseek/deepseek-chat`). Registers the model on the LLM gateway; the row starts as `draft`.
3. **Test** — the consumption-mirror check: mints a scoped key (budget 1 / rpm 1), asserts `200`,
   then `429` (rate limit), then `401/403` after revoke.
4. **Publish** — enabled only once the test passes → `status=published`.

**Verify:** the model row reads **published**. Only published models are offered to projects.

### 1.2 Review and publish base templates

**Base templates** page (`/admin/base-templates`): edit a base template's HCL + image, **Save
draft**, then **Publish** (version increments, content hash re-stamped).

Three bases ship seeded, one per infrastructure flavor — `dev-workspace` (standard),
`gpu-workspace`, `microvm-workspace`. They are **infra-only**: none names or bakes in a coding
agent. The agent is chosen per-flavor by the project-admin (§2.4) from the allow-list you set in
§1.3. Seeds are re-created on portal boot only when absent, so your edits are never overwritten.

A base body may only reference `${…}` placeholders from the known set; anything else is rejected at
publish. They fill in two passes:

- **At project-template create** — `namespace`, `image`, `git_repo_url`, `wif_role`,
  `vault_namespace`, `ssh_ca_path`, `github_token_path`, `mcp_kv_path`.
- **At each workspace launch** — `job_name`, `ssh_port`, `volume_name`, `developer_email`,
  `git_user_name`, plus `shared_volume_defs` / `shared_volume_mounts` (empty strings when the
  developer selects no shared volumes, so the job renders unchanged).

**Verify:** the page lists exactly the three bases, each `published` with an incremented version.

### 1.3 Coding agents allow-list

**Coding agents** page (`/admin/coding-agents`, platform-admin only). Each known agent has an
**enable** toggle and a governance note:

- **Claude Code** — governed egress through the project's LLM gateway virtual key.
- **IBM Bob** — the model runs on IBM's **hosted** backend, reached with the developer's IBMid, so
  it bypasses the per-project virtual key, budgets and guardrails. Surfaced as its governance note
  so a project-admin picks it knowingly.

Both are enabled by default; only *disabled* agents are persisted. The enabled set drives which
agents a project-admin may pick at flavor create.

**Verify:** toggle **Bob off** → as a project-admin the flavor-create *Coding agent* select shows
Claude only. Toggle it back on → Bob reappears.

### 1.4 Create a project

**Projects (admin) → New project**: name `project-acme` (must match
`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$`), developers group `project-acme-developers`, workspace user
`dev`, first admin email. **Create.**

One action provisions the project's whole footprint (idempotent): its Vault child namespace and WIF
roles, its Nomad namespace + ACL policy + OIDC binding rule, its Boundary project scope and
workspace host catalog, its Postgres descriptor and first-admin grant, and the standard engines —
SSH CA, `github` mount (credentials still to be filled in at §2.1), the WIF read policies, the
project's LLM gateway virtual key, and the Boundary credential store + SSH-cert library.

**Verify:** the project appears as a row in the **Projects (admin)** table; open its **Details** and
confirm **Status** is a green `ready` tag, with the Boundary scope and credential library populated.
The project-admin's engine checklist (§2.1) shows ssh / llm / boundary green.

> If project creation returns OK but the new project has no engines, the Portal's onboarding plane
> is disabled — see [README §7](./README.md#7-troubleshooting). Projects created while it was
> disabled must be deleted and recreated; engines do not back-fill.

---

## 2. Project-admin

Sign in as the first project-admin (must be in `project-acme-developers`). The project switcher
shows **members / mcp servers / templates / github access / shared volumes / agent templates**.

### 2.1 Set up GitHub credentials

This is what lets `git push` work inside a workspace with no pasted credential: Vault mints a
short-lived GitHub App installation token per workspace. The Portal only stores the App's
coordinates — you create the App on GitHub first.

**One-time, on GitHub.** You need org-owner (or personal-account) rights. An App may be shared across
projects or created per project; the credentials are written into each project's own Vault namespace
either way.

1. **Create the App** — org: `https://github.com/organizations/<org>/settings/apps/new`; personal:
   **Settings → Developer settings → GitHub Apps → New GitHub App**. Give it a unique name. Homepage
   URL can be anything; **untick Webhook → Active** (unused here).
2. **Permissions** — under **Repository permissions** set **Contents: Read and write**. This is the
   upper bound for every token the App can ever mint, and the Portal's permission set requests
   exactly `contents: write`; anything less makes token minting fail at workspace start. Leave the
   rest at *No access*.
3. **Where can this App be installed** — *Only on this account*.
4. **App ID** — shown at the top of the App's settings page after creation (**General → About**).
   A short integer.
5. **Private key** — **General → Private keys → Generate a private key**. The browser downloads a
   `.pem` **once**; GitHub never shows it again. It is PKCS#1 (`-----BEGIN RSA PRIVATE KEY-----`),
   which is the format the Vault plugin requires. If yours starts with `-----BEGIN PRIVATE KEY-----`
   (PKCS#8) convert it:

   ```bash
   openssl rsa -in downloaded.pem -out app-key-pkcs1.pem
   ```

6. **Install the App** — **Install App → Install** on the account that owns the repos, choosing
   *All repositories* or *Only select repositories*. A repo outside the installation cannot be
   granted a token later, whatever you type in step 8.
7. **Installation ID** — the trailing number in the URL you land on after installing, also reachable
   via **Install App → ⚙ (Configure)**:

   ```text
   org:      https://github.com/organizations/<org>/settings/installations/<installation_id>
   personal: https://github.com/settings/installations/<installation_id>
   ```

   It is a different, longer integer than the App ID — mixing the two is the usual mistake.

**In the Portal.** Open **github access** and fill:

8. **GitHub App ID** (step 4) · **GitHub App installation ID** (step 7) · **GitHub App private key**
   — paste the whole PEM including the `BEGIN`/`END` lines (write-only: it goes straight to Vault,
   is never echoed back, and the field clears on save) · **Repository names** — optional,
   comma-separated. Bare names are what GitHub expects (the owner is implied by the installation),
   but `owner/repo`, `https://github.com/owner/repo.git` and `git@github.com:owner/repo.git` are all
   accepted and reduced automatically. Leave empty to allow every repo in the installation.
   → **Save GitHub credentials**.

**Verify:** the **GitHub App** tag turns green `configured` and the checklist's *GitHub App broker
(github/)* row flips to `ready — mints ephemeral push tokens`, alongside ssh / llm / boundary green.
The App ID and installation ID prefill on reload; the private key does not. Real proof comes at §3.3,
where `git push` from inside a workspace succeeds.

### 2.2 Deploy MCP servers

**mcp servers → Deploy MCP server**. Deploy both examples below — one per credential model — then
**Test** each. Each lands as a job in the project's Nomad namespace and is registered as a gateway
peer automatically.

**1. `everything-mcp` — static credential**

| Field | Value |
|---|---|
| Name | `everything-mcp` |
| Image | `tzolov/mcp-everything-server:v3` |
| Transport | `streamable-http` |
| Container port | `8081` |
| Credential source | `static` — keep the prefilled `api_key=${api_key}` and its env template; the masked `api_key` value can be any string |

**Command args** (one per line):

```text
node
dist/index.js
streamableHttp
```

**Env vars** (`KEY=value`, one per line):

```text
PORT=8081
```

> The command args are **required**. With none, the image starts in STDIO mode with no HTTP listener
> and Test hangs ~30 s. The container port must equal `PORT`.

**2. `vault-mcp` — WIF-token credential**

| Field | Value |
|---|---|
| Name | `vault-mcp` |
| Image | `hashicorp/vault-mcp-server:latest` |
| Transport | `streamable-http` |
| Container port | `8082` |
| Credential source | `wif-token` — leave **Additional Vault paths** empty |

**Command args** (one per line):

```text
/bin/vault-mcp-server
http
```

**Env vars** (`KEY=value`, one per line — substitute the real IP into `VAULT_ADDR`):

```text
TRANSPORT_HOST=0.0.0.0
TRANSPORT_PORT=8082
VAULT_ADDR=https://<node-private-ip>:8200
VAULT_SKIP_VERIFY=true
```

**Retrieving `<node-private-ip>`.** There is no placeholder for it — the wizard only expands
`${...}` tokens that name credential parameters, so the address must be typed literally. It is the
**private** IP of the all-in-one node, because this server runs on the `agents` pool and reaches
Vault across the VPC. A project-admin cannot read it from the Portal; ask your platform operator,
who gets it from the `terraform/infra` directory:

```bash
terraform output -raw instance_private_ip
```

Or, without the Terraform state (`<owner>` is the `owner` tfvars value):

```bash
aws ec2 describe-instances --region <region> \
  --filters "Name=tag:Name,Values=<owner>-boundary" "Name=instance-state-name,Values=running" \
  --query "Reservations[].Instances[].PrivateIpAddress" --output text
```

The address survives a stop/start of the node, but a rebuild replaces the instance and issues a new
one — after any clean-slate rebuild, re-check this value and update the server (**Edit** on the row
restarts it with the new env).

> Do **not** set `VAULT_TOKEN` or `VAULT_NAMESPACE` — Nomad injects both from the job's workload
> identity. The derived policy grants `secret/data/projects/*` read plus `sys/mounts` read.

**3. Test each server.** Each row shows three status columns — **Status** (`deployed`, later
`published` once a template references it), **Running** (green once the alloc is healthy), and
**Test** (`untested` until you run it).

Wait for **Running** to go green, then click **Test** in the row's Actions column. Test one server
at a time — the other row actions are disabled while it runs. The check reconciles the gateway peer
with the live job placement (so the same button that reports a stale peer also repairs it),
discovers the server's tools, then mints a throwaway scoped token and asserts three things: the
token reaches **its own** server (`200`), is **denied** the admin surface (`403`), and is **denied**
a decoy server (`403`). It passes only if all three hold **and** at least one tool was discovered.

**Verify:** the **Test** column shows a green **passed (N tools)** tag for both servers. **View** on
a row shows the wiring the test reconciled — gateway peer URL, Nomad job, credential source.

> A passing `vault-mcp` test is the workload-identity proof point: the server started with **no**
> static Vault token and its tools were still discoverable through the gateway.
>
> Test's probe scaffolding is torn down afterwards, so the gateway's **Virtual Servers** list is
> still empty at this stage. The persistent per-server virtual server and scoped token are created
> in §2.4, when you wire the server into a template.

### 2.3 Assign the project-user role

**members** page: grant by email with role **project-user**. The grant stays inert until that email
is in `project-acme-developers`. The **Role capabilities** matrix at the bottom controls
`workspaces` / `ai-agents` per role (both on by default; the admin row is locked).

**Verify:** the member appears with role `project-user`; signing in as them shows the project.

### 2.4 Publish a workspace template with MCP add-ons

**templates → New template**: pick base `dev-workspace`, a **coding agent** (Claude Code or IBM Bob,
from the §1.3 allow-list), your Git repo, and a label. The image is read-only — it is pinned by the
base. The selected agent's governance note shows inline and each flavor row is badged with its
agent.

Then **Edit add-ons** → check `everything-mcp` + `vault-mcp` → **Apply**. This gives each server its
own virtual server, a scoped 365-day token, and a Vault KV entry the workspace reads at launch, then
re-renders the template with the injected secret blocks and MCP setup lines.

**Verify:** the template row carries an `N MCP` tag; the Details modal's rendered source contains
the injected blocks.

> To test both coding agents, create a second flavor from the *same* base with agent = IBM Bob.
> Both bake cleanly; the workspace-side difference is verified in §3.7.

### 2.5 Create shared volumes

**shared volumes** page — named EFS volumes that mount into the project's workspaces and come
**pre-selected** on the workspace-create modal. Each volume is its own EFS access point (own root
subtree, enforced POSIX uid), so volumes are isolated from each other and from other projects.

Create two, to exercise both access modes:

1. **`cache`** — read-**write**, mount path **`/shared/cache`**. This literal path matters: the
   workspace entrypoint auto-exports `GOMODCACHE` / `GOCACHE` / `npm_config_cache` / `PIP_CACHE_DIR`
   underneath it. Backs the write test in §3.3 and the warm-hit in §3.4.
2. **`datasets`** — read-**only**, mount path `/shared/datasets` (the default). Mounts read-only, so
   §3.3 can confirm writes are rejected.

> **`read_only` is immutable** — the API is create / list / delete only. To flip a volume's access,
> delete and recreate it (it must not be mounted by a running workspace at delete time). Access also
> binds **per launch**: changing a volume never re-mounts it into an already-running workspace.

**Verify:** both volumes list with the expected mount path and access mode.

---

## 3. Project-user (developer)

Sign in to the Portal as the developer (in `project-acme-developers`, granted project-user).

### 3.1 Create a workspace

**Projects → project-acme → New workspace** → pick the `dev-workspace` flavor. The modal's **Shared
volumes** section shows `cache` and `datasets` pre-checked; uncheck any you don't want → **Launch**.
The only choices are the flavor and the shared volumes — name, port and disk are server-generated.

**Verify:** the card reaches **running**.

### 3.2 Connect

**One-time (macOS): install the helper.** It is not on the workspace card — it lives two accordions
deep at the top of the project's **Workspaces** page. Expand **Connect to your workspaces**, then
expand **First time? Set up the Secured Workspace helper**, and click **Download the helper**. Run
the three commands shown there in Terminal:

```sh
unzip ~/Downloads/SecuredWS-macos.zip -d ~/Applications
xattr -dr com.apple.quarantine ~/Applications/SecuredWS.app
open ~/Applications/SecuredWS.app
```

The `xattr` line is not optional — the app is ad-hoc signed, so without it Gatekeeper blocks it as
"damaged" / "could not verify". Do **not** re-download afterwards: a fresh download re-applies the
quarantine flag. The final `open` registers the `secured-ws://` handler — the app has no UI of its
own, so it just confirms with a dialog that it is installed and quits.

macOS only — on Windows or Linux the panel says so and points at `portal/helper/README.md`.

After that, connecting is a single click:

**Open** — the helper checks for a usable Boundary token and, if there isn't one, runs the OIDC
login itself before continuing. With the Verify SSO session your Portal login already established,
that completes **silently** in the browser with no credential entry. It then writes a managed `Host`
block into `~/.ssh/config` (proxied through Boundary) and launches your IDE over Remote-SSH. You
land as `dev`.

The **Authenticate** button in the project's *Connect to your workspaces* panel is **not** a required
step. Use it only to pre-authenticate, or to recover when **Open** reports a sign-in error — it opens
a terminal running the same login interactively, where you can see what failed.

> Silent sign-in depends on the shared Verify session, so sign in to the Portal in a **normal browser
> window, not incognito**. From an incognito window Verify has no session to reuse and the login
> prompts on every connect.

CLI equivalent, without the Portal:

```sh
boundary authenticate oidc -addr <boundary-addr> -auth-method-id <auth-method-id> -tls-insecure
boundary connect ssh -target-name <workspace> -tls-insecure
```

### 3.3 Verify inside the workspace

All of this runs in the workspace terminal. Each block is independent.

```sh
# --- 1. Identity + per-launch secrets (no standing credentials) ---
whoami                 # dev  — landing here already proves Boundary + the Vault SSH CA;
                       #        access is a per-session signed cert, no key on disk
ls /secrets            # git-token, llm-key, mcp-<server>-url/-token — a tmpfs, never on /home/dev
```

```sh
# --- 2. Git — ephemeral GitHub App token, no PAT anywhere ---
cd ~/<your-repo>                       # pre-cloned from the template's repo
git config user.email                  # your email, baked in at launch
git pull
touch e2e-$(date +%s).txt && git add . && git commit -m e2e && git push   # succeeds
```

```sh
# --- 3. LLM — governed egress through the project's virtual key ---
# Managed settings live at /etc/claude-code/managed-settings.json, deliberately OUTSIDE /home/dev
# so they can't be edited to re-point the model. The key is read via Claude Code's apiKeyHelper
# and is never exported into the shell.
grep -E 'ANTHROPIC_BASE_URL|ANTHROPIC_MODEL' /etc/claude-code/managed-settings.json
claude -p "Say hello in one word"      # returns a completion, via the gateway

# Same path without Claude Code — proves the virtual key directly:
BASE_URL=$(grep -o '"ANTHROPIC_BASE_URL": *"[^"]*"' /etc/claude-code/managed-settings.json | cut -d'"' -f4)
MODEL=$(grep -o '"ANTHROPIC_MODEL": *"[^"]*"' /etc/claude-code/managed-settings.json | cut -d'"' -f4)
curl -s "$BASE_URL/v1/messages" \
  -H "x-api-key: $(cat /secrets/llm-key)" -H 'anthropic-version: 2023-06-01' \
  -H 'content-type: application/json' \
  -d "{\"model\":\"$MODEL\",\"max_tokens\":32,\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
```

```sh
# --- 4. MCP servers — one scoped registration per add-on from §2.4 ---
claude mcp list                        # everything-mcp + vault-mcp → both "✓ Connected"
cat /secrets/mcp-everything-mcp-url    # the scoped virtual-server URL the workspace consumes

# 4a. static-credential server — end-to-end tool call:
claude -p "Use the everything-mcp echo tool to echo the word: banana"

# 4b. WIF server — the proof-of-read. vault-mcp holds ONLY the Nomad-injected, namespace-scoped
# Vault token; its policy grants secret/data/projects/* read plus sys/mounts read.
claude -p "Using vault-mcp, read the secret at secret/projects/mcp-secrets/everything-mcp"
#  → returns the api_key set when deploying everything-mcp (§2.2)
```

> **vault-mcp read gotchas**
> - Use the **logical** path (`secret/projects/…`), not the API path (`secret/data/projects/…`). The
>   server inserts `data/` itself, so a pasted `data/` doubles up and the read silently returns
>   nothing.
> - Claude's permission classifier may block the tool call client-side before it ever reaches Vault
>   — that is not a Vault 403. Approve the tool and re-run.
> - *Listing* secrets additionally needs `secret/metadata/projects/* list,read`, which is not in the
>   default policy. "Mounts visible but no secrets listed" is the expected read-only shape, not a
>   failure. Grant it via the deploy wizard's **Additional Vault paths** if you need listing.

```sh
# --- 5. Shared volumes (additive to the per-workspace /home/dev) ---
mount | grep /shared                          # /shared/cache (rw) + /shared/datasets (ro)
touch /shared/cache/hello.txt                 # read-write volume → succeeds
touch /shared/datasets/hello.txt              # read-only volume → "Read-only file system" (expected)
cat /etc/profile.d/shared-cache.sh            # the auto-exported cache env vars
```

**Pass** = SSH lands as `dev`; `git push` succeeds; both the `claude -p` call and the raw
`/v1/messages` curl return a completion; `claude mcp list` shows both servers connected; the
`vault-mcp` read returns the seeded `api_key`; and `/shared/cache` accepts a write while
`/shared/datasets` rejects one.

### 3.4 Shared-volume cache warm-hit *(optional)*

The point of the `cache` volume: a cold dependency fetch in one workspace warms the cache for the
next, even on a different node.

```sh
# Workspace A (cold): populates /shared/cache/go
time (cd ~/<go-repo> && go mod download)      # slow — network fetch
ls /shared/cache/go/cache/download

# Launch workspace B (same project, cache checked), then in B:
time (cd ~/<go-repo> && go mod download)      # fast — served from the shared cache, no refetch
```

**Pass** = the second download is materially faster and does no network fetch. Deselect `cache` on a
third workspace and it launches with no `/shared/cache` at all — the feature is opt-out per launch.

### 3.5 Cross-project isolation *(optional)*

A second project cannot see or mount project-acme's shared volumes:

- Create `project-beta` (§1.4). As its project-admin, the **shared volumes** page lists none of
  project-acme's volumes.
- A `project-beta` workspace's create modal offers only `project-beta` volumes. Each volume's EFS
  access point is a hard chroot root with its own enforced uid, so there is no path across.

### 3.6 Reachability across a node change *(optional)*

A workspace stays reachable if it moves to another node: its `/home/dev` volume follows it, and the
host-sync reconciler repoints the workspace's **stable** Boundary target at the new node address —
no reconnect, no target change, same connection details.

To exercise it on a single-node cluster, set `enable_default_spare = true` (README §8) to add a
temporary second node, force a reschedule, confirm the workspace is still reachable, then set the
flag back to `false`.

### 3.7 Bob flavor *(optional)*

Everything in §3.3 that is **infrastructure** rather than agent — block 1 (identity + secrets),
block 2 (git), block 5 (shared volumes) — behaves identically on a Bob-flavor workspace: same base
image, same entrypoint, same per-launch secrets. What differs is the coding agent, so blocks 3 and 4
are replaced by the checks below.

Create a workspace from the Bob flavor (§2.4), connect (§3.2), then:

```sh
# --- 1. The agent itself. Bob's model runs on IBM's HOSTED backend, not the governed gateway ---
command -v bob && bob --version         # bob on PATH
node --version                          # Bob requires Node >= 22.15
ls /etc/claude-code/managed-settings.json 2>/dev/null || echo "absent (expected for Bob)"
ls /secrets/llm-key 2>/dev/null         || echo "no llm-key (expected for Bob)"
bob                                     # first run → interactive IBMid sign-in, then the REPL
```

```sh
# --- 2. MCP add-ons, wired into Bob's own config instead of Claude's ---
jq '.mcpServers | keys' ~/.bob/mcp_settings.json       # ["everything-mcp","vault-mcp"]
jq '.mcpServers["everything-mcp"]' ~/.bob/mcp_settings.json
cat /secrets/mcp-everything-mcp-url                    # matches the .url above
```

Inside the Bob REPL, exercise the same read as the Claude flavor:
`Using vault-mcp, read the secret at secret/projects/mcp-secrets/everything-mcp` — the same
logical-path gotcha from §3.3 applies, since the server is identical and only the agent differs.

**Pass** = `bob --version` prints; there is **no** managed-settings file and **no** llm-key (Bob is
off the governed LLM path by design); and `~/.bob/mcp_settings.json` lists every add-on server with
a URL and bearer token matching the `/secrets/mcp-<server>-*` files.

---

## 4. AI-agents plane *(optional)*

Any member with the **ai-agents** capability can author, deploy and chat with a YAML agent.

**Projects → project-acme → AI agents → New agent.** The editor pre-fills a skeleton — name,
instructions, a published `llm:` model, `tools.mcp_servers` (the project's deployed MCP servers),
and `tools.builtins`. **Validate** checks it server-side; **Deploy** mints a per-agent LLM key,
grants the agent its workload identity, and starts it on the agents pool.

**Verify:** the agent reaches **running**; **Test** reports a healthy probe and a tool count; **Chat**
streams tokens live, surfacing MCP tool calls as "running tool: X".

**Capability gate:** untick `ai-agents` on the project-user row (§2.3) → that member gets 403 on
every agents route.

---

## 5. Delete a project

**Projects (admin) → project-acme → Delete project…**, typing the name to confirm. One action tears
down everything the project owns: running workspaces and MCP jobs together with their per-workspace
and shared volumes, the Nomad namespace and ACLs, the Vault child namespace, the Boundary scope
(recursively, so the workspace host catalog goes with it), the gateway peers and scoped tokens, the
project's LLM virtual key, and the Portal's own rows.

Irreversible — workspace home directories are destroyed. A partial failure leaves the project at
`status=error`; deleting again retries until it converges.

**Verify:** the project disappears from the **Projects (admin)** table, and from the project switcher
of the project-admin and project-user who held it.

---

## Appendix A — operator backend verification

Optional. These commands prove the backend state behind each UI step — they need **operator**
credentials (Vault root token, Nomad management token, AWS) that **no Portal role holds**. Skip this
appendix entirely if you are testing the roles as a user.

```sh
cd terraform/infra
export VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true
export VAULT_TOKEN="$(terraform output -raw vault_root_token)"
export NOMAD_ADDR="$(terraform output -raw nomad_addr)" NOMAD_SKIP_VERIFY=true
export NOMAD_TOKEN="$(terraform output -raw nomad_management_token)"
export PORTAL="$(terraform output -raw developer_portal_addr)"
export ORG_SCOPE_ID="$(terraform output -raw org_scope_id)"
export LLM="$(terraform output -raw llm_gateway_addr)"
export LLM_MASTER="$(vault kv get -field=master_key secret/infra/llm-gateway)"
# Region + owner must match terraform.tfvars; the EFS filesystem is "<owner>-boundary-shared".
export AWS_REGION="ap-southeast-1"
export OWNER="rp"
export FS_ID="$(aws efs describe-file-systems --region "$AWS_REGION" \
  --query "FileSystems[?Name=='${OWNER}-boundary-shared'].FileSystemId" --output text)"
```

The `--cookie "session=<…>"` in the Portal API calls below is a browser session cookie copied from
your signed-in dev tools.

**§1.1 — LLM model onboarded**

```sh
vault kv get secret/infra/llm-providers/deepseek                       # provider key present
curl -s "$LLM/model/info" -H "Authorization: Bearer $LLM_MASTER" | jq '.data[].model_name'
curl -sk "$PORTAL/api/admin/llm/models" --cookie "session=<…>" | jq '.models[] | {name,status}'
```

**§1.2 / §1.3 — base templates and the agent allow-list**

```sh
curl -sk "$PORTAL/api/admin/base-templates" --cookie "session=<…>" \
  | jq '.[] | {name, version, status}'          # exactly three, all published
curl -sk "$PORTAL/api/admin/coding-agents" --cookie "session=<…>" \
  | jq '.agents[] | {key, enabled}'
```

**§1.4 — project footprint**

```sh
VAULT_NAMESPACE=project-acme vault secrets list                        # ssh/ github/ secret/
VAULT_NAMESPACE=project-acme vault read auth/jwt-nomad/role/project-acme
VAULT_NAMESPACE=project-acme vault kv get secret/projects/llm          # base_url + virtual_key
nomad namespace list | grep project-acme
boundary scopes list -scope-id "$ORG_SCOPE_ID" -tls-insecure           # the project scope
ACME_SCOPE=$(boundary scopes list -scope-id "$ORG_SCOPE_ID" -tls-insecure -format=json \
  | jq -r '.items[]|select(.name=="project-acme").id')
boundary host-catalogs list -scope-id "$ACME_SCOPE" -tls-insecure      # dev-workspaces
curl -s "$LLM/key/info?key_alias=llm-project-acme" \
  -H "Authorization: Bearer $LLM_MASTER" | jq '.info.key_alias'
```

**§2.2 — MCP servers deployed and isolated**

```sh
nomad job status -namespace project-acme mcp-project-acme-everything-mcp   # running
nomad job status -namespace project-acme mcp-project-acme-vault-mcp        # running

# The Test verdict behind the green "passed (N tools)" tag:
curl -sk "$PORTAL/api/projects/project-acme/mcp-servers" --cookie "session=<…>" \
  | jq '.[] | {name, status, test: (.test_result |
      {passed, tools_discovered, own_server_ok, admin_denied, other_server_denied})}'
#  want each → passed:true, tools_discovered>0, and all three access assertions true
```

**§2.4 — gateway wiring after add-ons are applied**

Each MCP server gets its **own** peer, virtual server (bundling only that server's tools) and scoped
token, all named `mcp-project-acme-<server>`. There is no combined per-project virtual server, so
each is independently scoped and revocable.

Gateway Admin UI: `terraform output -raw mcp_gateway_addr` → `/admin`, logging in with
`vault kv get secret/infra/mcp-gateway` (`admin_email` / `admin_password`). Or close the loop
against the Vault entry the workspace actually consumes:

```sh
vault kv get -namespace=project-acme -field=url secret/projects/mcp/everything-mcp
# → http://<node-ip>:4444/servers/<virtual-server-id>/sse
```

**§2.5 — shared volumes**

```sh
nomad volume status -namespace project-acme        # shared-project-acme-cache, -datasets
aws efs describe-access-points --region "$AWS_REGION" --file-system-id "$FS_ID" \
  --query 'AccessPoints[].{Id:AccessPointId,Root:RootDirectory.Path,Uid:PosixUser.Uid}' --output table
```

> **Cap:** EFS allows 120 access points per filesystem = 120 shared volumes platform-wide.

**§5 — teardown is complete**

```sh
nomad namespace list                                        # project-acme gone
nomad volume status -namespace project-acme                 # empty
boundary scopes list -scope-id "$ORG_SCOPE_ID" -tls-insecure
aws efs describe-access-points --region "$AWS_REGION" --file-system-id "$FS_ID"
curl -s "$LLM/key/info?key_alias=llm-project-acme" -H "Authorization: Bearer $LLM_MASTER"
```
