# Developer tier — one workspace per developer (`terraform/workspace/`)

The **developer tier** of the three-tier model (`terraform/{infra,project,workspace}`). A **flat root** (no child module) applied **once per developer-workspace**, using a separate **Terraform workspace** for each. It only talks to Nomad + Boundary + Vault over the NLB, so it **re-applies without ever touching the foundation instance**.

All connection details and project wiring are read automatically from the **foundation** and **project** states via `terraform_remote_state` (see `providers.tf`) — **no tokens or addresses go in tfvars**. Per-workspace facts (who, which project, which port, which repo) are the only inputs.

What each instance creates:

- a **persistent workspace container** in the project's Nomad namespace (Docker, sshd, build tools) on a **dynamic host volume** mounting `/home/dev` so it survives stop/start + reboot — rendered from the project's **Vault-KV job template** via `templatestring()`;
- a first-boot clone of **the flavor's pinned git repo** into `/home/dev/project` (the repo is set per-template in the project tier's `workspace_templates`, not chosen here) — **private github.com repos supported**, with **git pre-configured** for the logged-in developer (author = their email) and a **short-lived, Vault-minted GitHub App token** as the push credential (rendered to tmpfs, auto-refreshed, never on the persistent volume);
- a **Boundary ssh target** + **global alias** for the workspace;
- a **per-workspace OIDC managed group** (matched on the developer's `/token/email`) + a **role** granting `authorize-session` on **only this target**, plus a self-scoped `list-resolvable-aliases` role for the Client Agent;
- a generated `~/.ssh/config` snippet under `generated/<handle>-<workspace>.ssh-config`.

SSH auth is **JIT Vault-signed certificates injected by Boundary** — the developer holds **no SSH key**. The workspace `sshd` trusts only the project's Vault SSH CA (`TrustedUserCAKeys` + `AuthorizedKeysFile none`); on each session Boundary signs a short-lived (5m) ed25519 cert via the **project's** credential library and injects it. The CA public key reaches the container over Nomad↔Vault workload identity. Identity is the existing **IBM Verify OIDC** login — no password accounts; the `/token/email` claim is also stamped as the cert `key_id` for audit.

> **Scope (PoC):** single all-in-one node + an optional GPU worker + an optional bare-metal **microVM worker** (Kata Containers — hardware isolation for the `microvm-workspace` flavor). Standard and GPU flavors use Docker isolation; the microVM flavor adds a KVM boundary. **Private-repo clone/push via Vault-minted GitHub App tokens is implemented** (the project must have a GitHub App configured — see `terraform/project`). The **Developer Portal** (`portal/`) is a working PoC that provisions the same workspaces over the HashiStack APIs directly — this Terraform tier and the portal are two paths to the **same** Vault-KV job templates. Durable storage (CSI/EBS) and multi-node remain roadmap.

## Prerequisites

1. **Foundation applied and running** (`terraform/infra/`) and **Vault unsealed** — the workspace `sshd` trusts the Vault SSH CA and Boundary signs each session's cert through Vault, so a sealed Vault breaks login. Re-unseal after a reboot (see `terraform/infra/README.md`).
2. **The project applied** (`terraform/project/`) under a Terraform workspace **named after the project**, e.g. `project-acme`. That tier creates the Boundary project scope, the Nomad namespace, the per-project Vault SSH CA + signing role, the shared Boundary credential library, the WIF role, the **GitHub App token broker** (the git push credential source), and writes the project's **job templates (flavors) + their pinned images to Vault KV**. This tier reads that project's state at `../project/terraform.tfstate.d/<project_name>/terraform.tfstate`.
3. **A workspace flavor published by the project.** The project tier **owns each flavor's image AND git repo**: every job template ("flavor") is built and pushed by the project team and pinned — in `workspace_templates` — to its image, its git repo, and an optional `node_pool` (the build steps live in `terraform/project/README.md`). The developer just picks a flavor via `job_template_name` (default `dev-workspace`) — **there is no image or repo to set here**, so every workspace in the project runs the project-blessed toolchain against the flavor's repo. A flavor with `node_pool = "gpu"` (e.g. `gpu-workspace`) is placed on the GPU node automatically (see [GPU flavor](#gpu-flavor) below); a flavor with `node_pool = "microvm"` (e.g. `microvm-workspace`) is placed on the bare-metal Kata-microVM node automatically (see [microVM flavor](#microvm-flavor) below).
4. **An IBM Verify developer** whose `email` claim matches `developer_email`, able to log in through the existing Boundary OIDC method.

## Apply (one Terraform workspace per developer-workspace)

```bash
cd terraform/workspace
terraform init
cp terraform.tfvars.example alice-main.tfvars   # edit (gitignored)
terraform workspace new alice-main
terraform apply -var-file=alice-main.tfvars
```

`alice-main.tfvars` holds only per-workspace facts:
```hcl
developer_email  = "alice@example.com"   # IBM Verify /token/email — managed-group filter + cert key_id
developer_handle = "alice"
workspace_name   = "main"
project_name     = "project-acme"        # must match an applied project workspace of this name
ssh_port         = 2222                   # DISTINCT static host port per workspace on the shared node
# job_template_name = "dev-workspace"     # optional: the project flavor (image AND repo pinned to it); default shown
```

There is **no `git_repo_url` or `image`** here — both are pinned to the chosen flavor by the project tier (`workspace_templates`), so the developer tier renders only the per-workspace placeholders (`job_name`, `ssh_port`, `volume_name`, `developer_email`, `git_user_name`) — the **same set the Developer Portal fills**, from the **same** published template.

Connection inputs are **not** here either — `providers.tf` reads `nomad_addr`, `boundary_addr`, `vault_addr`, the admin/management/root credentials, `boundary_oidc_auth_method_id`, and the node IPs (`instance_private_ip` + `gpu_instance_private_ip` + `microvm_instance_private_ip`) from the foundation state, and the project scope, namespace, credential library, WIF role, SSH CA path, and the per-flavor `job_template_node_pools` map from the project state.

> **Apply order:** foundation → project (its workspace) → here. Each new developer-workspace is `terraform workspace new <handle>-<name>` + `apply -var-file=<handle>-<name>.tfvars`.

> **⚠️ No `-auto-approve` against shared Boundary/Nomad/Vault.** Use a saved, reviewed plan: `terraform plan -out=<name>.tfplan -var-file=<name>.tfvars`, review, `terraform apply <name>.tfplan`, then **delete the plan file** (`*.tfplan` and `*.tfvars` embed secrets and are gitignored — never commit).

After apply, outputs give you the connect handles:
```bash
terraform output            # alias, target_id, connect_command, ssh_config_path, namespace, ssh_port
```

## Verify the Boundary-brokered workspace SSH

Both connect paths are **live-verified (2026-06-02)**: the CLI `boundary connect ssh` (cert injection) and plain `ssh <alias>` / VSCode Remote-SSH via the Boundary Client Agent (transparent sessions, next section).

**1. SSO-authenticate to Boundary as the developer** (the same IBM Verify login):
```bash
eval "$(terraform -chdir=../infra output -raw boundary_oidc_login_command)"   # browser OIDC flow
```
> The OIDC method sets `prompts = ["login"]`, so IBM Verify **forces a fresh login every time** — enter the right developer's credentials here. No `boundary logout` needed to switch identities (and it errors against the self-signed controller cert anyway). (`select_account` alone does NOT force re-auth — Verify reuses the existing session.)

**2. Connect with `boundary connect ssh`** (routes through the co-located worker proxy on 9202 — nothing is exposed on the NLB). Boundary signs a fresh Vault cert and injects it; there is **no local key and no `-i`**:
```bash
boundary connect ssh -tls-insecure -target-id "$(terraform output -raw target_id)" -- whoami
# → dev  (proves cert injection logs you in)
```
The brokered cert's `key_id` is the authenticated developer's email — confirm in the workspace `sshd` stderr (`nomad alloc logs -stderr -namespace <project> <alloc> workspace`).

> Because each new SSH connection re-invokes Boundary and gets a **fresh** cert, the 5m cert TTL only covers the handshake while the target's `session_max_seconds` (8h) governs session length.

### Verification gates

1. **Static:** `terraform fmt -recursive -check && terraform validate` here and in `../infra`, `../project`.
2. **Image:** the project-published flavor image (pinned to `job_template_name`, e.g. `<dockerhub-user>/dev-workspace:poc`) `docker pull`s from a logged-out client — it is PUBLIC and the **project** owns/builds it (see `../project/README.md`).
3. **Namespace:** `nomad namespace list` shows the project namespace.
4. **Job healthy:** `nomad job status -namespace <project> ws-<handle>-<name>` is running; `nomad alloc logs <alloc>` shows `Server listening on 0.0.0.0 port 22`.
5. **Cert-only sshd:** in the container `/etc/ssh/trusted_ca.pub` is present and `grep -E '^(TrustedUserCAKeys|AuthorizedKeysFile)' /etc/ssh/sshd_config` shows the CA path **and** `AuthorizedKeysFile none`.
6. **Clone present:** `/home/dev/project/.git` exists and `git -C /home/dev/project remote -v` points at the **flavor's pinned repo** (`workspace_templates[<flavor>].git_repo_url` in the project tier) — a **private** repo proves the GitHub App token clone worked. 6a. **Git identity + push:** in the workspace, `git config --global -l` shows the developer's `user.email`/`user.name`; `/secrets/git-token` exists on tmpfs (not under `/home/dev`); and from `/home/dev/project` an edit + `git commit` + `git push` succeeds with **no credential prompt** (author = the developer, pusher = the App bot). A push **after >1h** in one session still works — consul-template re-mints the token before expiry. 6b. **DB MCP (use case B1):** `/secrets/mcp-url` and `/secrets/mcp-token` exist on tmpfs (not under `/home/dev`) and hold the project's **virtual MCP server URL on the central ContextForge gateway** plus a **per-project scoped client bearer token** (both rendered over WIF from `secret/projects/<project>/mcp`); the workspace itself holds **no DB URI** — it never talks to Postgres directly. `sudo -u dev claude mcp list` shows `demo-db` as a **remote SSE server** pointing at the gateway URL, `✓ Connected` (the old local `/usr/local/bin/pg-mcp` stdio server is gone). A `SELECT` through that remote MCP server returns the seeded rows while `INSERT`/`UPDATE`/`DROP` are refused — at **both** the demo-db-mcp service's `--access-mode=restricted` layer and the DB-level SELECT-only grant (the Vault-dynamic read-only role now lives on the **demo-db-mcp service** side, not the workspace). See `docs/specs/vault-usecase-b1-db-mcp-dynamic-creds.md`, and the step-by-step TUI walkthrough under **Exercising gate 6b in the `claude` TUI** below. 6c. **LLM gateway (Claude Code → provider via the gateway):** `/secrets/llm-key` exists on tmpfs (not under `/home/dev`) and holds the project's **LiteLLM virtual key** — **not** the real provider key (rendered over WIF from `secret/projects/<project>/llm`). `/etc/claude-code/managed-settings.json` (enterprise-managed, installed at job launch) points `ANTHROPIC_BASE_URL` at the node-private LiteLLM gateway and maps the model slots to `deepseek-v4-pro`/`deepseek-v4-flash`; Claude's `apiKeyHelper` (`/usr/local/bin/llm-key`) serves the virtual key. `sudo -u dev claude -p "say hi"` responds — the call traverses the **central gateway** (visible in its spend logs, tagged `llm-<project>`) and the workspace never holds the provider key; a request for a model outside the key's scope is **403**. The real provider key lives only on the gateway (infra tier) — see [`../infra/README.md`](../infra/README.md#verify-the-llm-gateway).
7. **Boundary positive:** as the developer, the `boundary connect ssh` above logs in with **no local key**; the cert `key_id` = the developer's email in the `sshd` stderr log.
8. **Boundary NEGATIVE (required):** SSO-authenticate as a **different, non-admin** developer, then `boundary connect ssh -target-id <the first developer's target>` is **DENIED** — their managed group has no grant on the other target. (Use a developer who is **not** in the admin managed group — an admin has `authorize-session` on all targets via the admin role's `descendants` grant, so an admin reaching both is expected and does not test isolation.)
9. **Reachability negative:** on the node `ss -tlnp | grep <port>` shows the SSH host port bound; from off-box `nc -vz <nlb-dns> <port>` **fails** — the only path in is Boundary.

### Exercising gate 6b in the `claude` TUI

Gate 6b above is the at-a-glance check; this is the operator walkthrough that drives the read-only path through the **actual** `claude` TUI. Seeded table is `employees` (3 rows: Ada Lovelace, Alan Turing, Grace Hopper) in `appdb`. SSH into the workspace as `dev` first.

1. **Pre-flight** — confirm the wiring before launching the TUI:
   ```bash
   ls -l /secrets/mcp-url /secrets/mcp-token && mount | grep /secrets  # gateway coords on tmpfs, not /home/dev
   claude mcp list                                                     # expect: demo-db (remote SSE) ... ✓ Connected
   ```
`✓ Connected` here already proves the path to the gateway's virtual MCP server; the TUI steps make it visible.

2. **SELECT through the TUI** — run `claude`, then prompt it to use the tool:
> Using the demo-db MCP server, list all rows in the `employees` table.

Expect the 3 seeded rows back (Ada / Engineer / 120000, Alan / Architect / 150000, Grace / Director / 180000).

3. **Write refused (negative)** — in the same session:
> Through the demo-db MCP server, insert a row into `employees` (name 'Test', role 'X', salary 1) — then tell me exactly what error came back.

Expect refusal at **both** layers: the MCP `--access-mode=restricted` rejects non-read SQL, and the DB grant is SELECT-only (`permission denied for table employees`).

4. **Dynamic DB credential (where it lives now)** — the workspace no longer renders a DB URI. The Vault-dynamic, SELECT-only Postgres role is held by the **demo-db-mcp service** (`terraform/project/demo-db-mcp.tf`), which sources it as `DATABASE_URI` over WIF (`postgresql://…@<node-ip>:15432/appdb`) and serves one shared connection for the whole project — auto-rotated on lease (24h default / 7d max, `change_mode=restart`), not minted per developer session. To inspect the live credential you look at that service, not the workspace; the workspace only ever sees the gateway URL + scoped token (see `terraform/project/database.tf`).

## GPU flavor

A project can publish a GPU flavor (`gpu-workspace`) whose `workspace_templates` entry sets `node_pool = "gpu"` and a CUDA-capable image. Selecting it here is a single line:

```hcl
job_template_name = "gpu-workspace"
```

Nothing else changes. The developer tier reads the flavor's pool from the project state (`job_template_node_pools`) and automatically (1) pins the `/home/dev` dynamic host volume to the `gpu` pool and (2) points the Boundary host at the **GPU node's** private IP (`gpu_instance_private_ip`, from the foundation state) instead of the main node — so the workspace lands on the `g4dn.xlarge` NVIDIA-T4 node. (The portal reaches the same outcome by resolving the placement IP from the live allocation; here the per-pool node is known up front.) The GPU node is a Nomad **client in the `gpu` node pool** with the `nomad-device-nvidia` plugin, provisioned by the platform tier — see [`../infra/README.md`](../infra/README.md#gpu-worker-node).

Verify inside the workspace, over the same Boundary SSH path:
```bash
nvidia-smi                              # the T4 is visible
cd ~/project && make && ./vectorAdd     # bundled CUDA sample -> "Test PASSED"
```

## microVM flavor

A project can publish a hardware-isolated flavor (`microvm-workspace`) whose `workspace_templates` entry sets `node_pool = "microvm"`. It runs the **same `dev-workspace` image** (no CUDA, no separate build) but inside a **Kata Containers microVM** — its own guest kernel behind a KVM boundary — for running untrusted AI-agent code with stronger isolation than shared-kernel containers. Selecting it here is a single line:

```hcl
job_template_name = "microvm-workspace"
```

Nothing else changes. The developer tier reads the flavor's pool from the project state (`job_template_node_pools`) and automatically (1) pins the `/home/dev` dynamic host volume to the `microvm` pool and (2) points the Boundary host at the **microVM node's** private IP (`microvm_instance_private_ip`, from the foundation state) — so the workspace lands on the `c5.metal` Kata node. The full credential stack (JIT SSH cert, Vault-minted GitHub App token, remote MCP, governed LLM, tmpfs secrets) is byte-identical to `dev-workspace`; only the runtime boundary is upgraded. The microVM node is a Nomad **client in the `microvm` node pool** provisioned by the platform tier — see [`../infra/README.md`](../infra/README.md#microvm-worker-node).

Verify inside the workspace, over the same Boundary SSH path — the proof is that the workspace runs a **different kernel than the host node** (a microVM, not a shared-kernel container):
```bash
uname -r                      # the Kata guest kernel (e.g. 6.1.62) — NOT the host node's kernel
mount | grep /home/dev        # 'virtiofs' — the home volume is shared into the guest over virtio-fs
```
If `uname -r` matched the host node's kernel, the task would have fallen back to `runc` (shared kernel) — a distinct guest kernel can only happen inside the Kata microVM. Everything else (`git push`, remote `demo-db` MCP, the governed LLM gateway) works exactly as in the standard flavor.

## VSCode Remote-SSH via transparent sessions

With the **Boundary Client Agent** the developer skips `boundary connect` entirely: the agent intercepts DNS for the target's **alias** and brokers + injects the cert transparently, so `ssh <alias>` (and VSCode Remote-SSH to a normal `Host`) just works. `boundary connect ssh` remains the no-Client-Agent fallback.

Per-laptop, one-time (the developer's action, like the OIDC login):

1. **Install the matching Client Agent + Desktop + CLI** (macOS or Windows only). Fully **uninstall** any existing Boundary Desktop/CLI first, then install the bundled installer from the releases page (the Client Agent ships with the Desktop installer). Confirm: `boundary client-agent status`.
2. **Trust the controller's self-signed cert on the laptop.** On macOS, import the controller leaf cert into the **System** keychain and mark it *Always Trust*:
   ```bash
   echo | openssl s_client -connect <nlb-dns>:9200 2>/dev/null \
     | openssl x509 > /tmp/boundary.crt
   sudo security add-trusted-cert -d -r trustRoot \
     -k /Library/Keychains/System.keychain /tmp/boundary.crt
   ```
The Client Agent validates the controller cert with Go's macOS platform verifier and has **no `-tls-insecure` escape**; see Troubleshooting for the 398-day cert constraint.
3. **Authenticate** as the right developer via the existing OIDC login:
   ```bash
   eval "$(terraform -chdir=../infra output -raw boundary_oidc_login_command)"
   ```
4. **Add a normal `~/.ssh/config` host entry** — **no `ProxyCommand`, no `IdentityFile`** (Boundary injects the cert). The Client Agent listens on the target's port — the workspace's static `ssh_port` — so set `Port` to match; a bare `ssh <alias>` would otherwise default to port 22 and reach nothing. The apply already generated this block:
   ```bash
   cat generated/<handle>-<name>.ssh-config >> ~/.ssh/config
   ```
   ```
   Host main.alice.project-acme.boundary
     Port 2222          # = this workspace's ssh_port (distinct per workspace on the shared node)
     User dev
     StrictHostKeyChecking no
     UserKnownHostsFile /dev/null
   ```
`StrictHostKeyChecking no` + `UserKnownHostsFile /dev/null` matter: the container's SSH **host key regenerates whenever the workspace is rebuilt** (it is not persisted on the `/home/dev` volume), so without these you hit `REMOTE HOST IDENTIFICATION HAS CHANGED` and must `ssh-keygen -R "[<alias>]:<port>"` after every rebuild.
5. **Connect:** `ssh main.alice.project-acme.boundary` logs in as `dev` with no key; or in **VSCode** → Remote-SSH: *Connect to Host…* → the alias → opens `/home/dev/project`. A benign `client_global_hostkeys_prove_confirm: ... bad signature` line may appear — it is the brokering proxy unable to prove the *upstream* host key during OpenSSH key rotation; the session is unaffected.

> **Alias resolution permission.** The Client Agent needs the self-scoped `list-resolvable-aliases` grant on the user resource to cache the aliases you can reach; the `dev_resolve_aliases` role (in `boundary.tf`) grants it. Self-scoped, so you only ever resolve aliases for targets you can already reach — isolation is unchanged. Vault must still be **unsealed** (the worker signs/injects the cert per session, same as the CLI path).

## Troubleshooting

**Repo isn't updated after the flavor's repo changed.** The repo URL is pinned to the flavor by the project tier (`workspace_templates[<flavor>].git_repo_url`), baked into the published job template — so changing it is a **project-tier** edit + re-apply (re-publishes the KV template), not a developer-tier var. On top of that the clone is **first-boot-only** and `/home/dev` **persists**:
```bash
if [ ! -d /home/dev/project/.git ]; then sudo -u dev git clone ${git_repo_url} /home/dev/project; fi
```
So even after the template is re-published, an existing workspace won't re-clone — the guard sees the existing `.git`. To pick up a new repo, recreate the volume **and** job so the workspace boots into an empty `/home/dev`:
```bash
terraform apply \
  -replace=nomad_job.workspace \
  -replace=nomad_dynamic_host_volume.home \
  -var-file=<name>.tfvars
```
(This wipes the whole `/home/dev`, not just `project/` — the VSCode server cache etc. re-provision on next connect. Boundary target/alias/managed-group are unaffected.)

**`Connection closed by 127.0.0.1 port <p>` / `boundary connect` opens but SSH immediately drops.** Nomad's docker driver publishes static host ports on the node's **primary private IP**, not `127.0.0.1`. `workspace_host_address` is read from the foundation `instance_private_ip` output, so this should match automatically; confirm on the node:
```bash
sudo ss -tlnp | grep ':2222'      # shows e.g. 10.220.10.196:2222, NOT 127.0.0.1
sudo journalctl -u boundary | grep 'connection refused'
```

**`boundary client-agent status` shows `not standards compliant` / `certificate … unknown authority` (Client Agent path only).** The Client Agent validates the controller's self-signed TLS cert with Go's macOS platform verifier and has **no `-tls-insecure` escape** (unlike the CLI). That verifier rejects any cert valid for more than **398 days** (golang/go#51991), so a long-lived self-signed cert fails even after you trust it. This is an **infra-side** fix: the foundation Boundary/Vault/Nomad certs are ≤398-day (`terraform/infra/modules/secured-codespace/tls.tf` sets 365 days) and the cert must be trusted on the laptop (step 2 above). After the cert is regenerated, fully restart the agent so it rebuilds its TLS root pool — it runs as a **system LaunchDaemon**, not a user agent:
```bash
sudo launchctl kickstart -k system/com.hashicorp.boundary.boundary-client-agent
```

**VSCode: "Failed to set up dynamic port forwarding" / "TCP port forwarding appears to be disabled".** The Vault signing role must issue certs with the `permit-port-forwarding` extension — VSCode Remote-SSH uses an SSH direct-tcpip channel. This is set in the project tier (`terraform/project/vault.tf`); because Boundary signs a fresh cert per connection, simply **reconnect** (Remote-SSH: *Kill VS Code Server on Host* then *Connect to Host* again) to pick up a cert with the extension.
