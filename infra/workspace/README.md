# Dev workspace — day-2 layer (`infra/workspace/`)

A **separate Terraform root** that deploys secured coding workspaces on top of the
already-running base stack. It only talks to Nomad + Boundary over the NLB, so it
**re-applies without ever touching the base instance** (no instance replace).

What it creates, driven by the `projects` and `developers` variables:

- a **Nomad namespace** per project;
- a **persistent workspace container** per developer-workspace (Docker, `code`
  tools, sshd), with a **dynamic host volume** mounting `/home/dev` so it survives
  stop/start + reboot;
- a first-boot clone of the project's **public** git repo;
- a **Boundary ssh target** per workspace and a **per-developer OIDC managed group
  + role** so a developer can `authorize-session` on **only their own** workspace.

SSH auth is **JIT Vault-signed certificates injected by Boundary** — the developer
holds **no SSH key**. The workspace `sshd` trusts only the Vault SSH CA
(`TrustedUserCAKeys` + `AuthorizedKeysFile none`); on each session Boundary signs a
short-lived (5m) ed25519 cert via the Vault credential store and injects it. The
CA public key reaches the container over Nomad↔Vault workload identity. See
`docs/specs/phase-2-jit-vault-ssh-certs.md`.

Identity is the existing **IBM Verify OIDC** login — no password accounts. Each
developer is matched by their `/token/email` claim (also stamped as the cert
`key_id` for audit).

> **Scope (PoC):** single node; Docker isolation; public-repo clone. Durable
> storage (CSI/EBS), private-repo clone via Vault, multi-node, microVM, and the
> developer portal are roadmap — see `docs/PLAN.md`.

## Prerequisites

1. **Base stack applied and running** (`infra/`), so `generated/nomad-setup.json`
   and the Boundary project scope exist.
2. **The image built locally and pushed to Docker Hub (multi-platform).** Build from
   the Dockerfile on your workstation and push a **public** manifest covering both
   `linux/amd64` and `linux/arm64`, so the image runs on the EC2 node (amd64)
   regardless of your build host's arch (e.g. Apple Silicon = arm64). Nomad pulls it
   at job launch (`force_pull = true`). Use your Docker Hub namespace in place of
   `<dockerhub-user>` and set the same value as the project's `image`:
   ```bash
   docker login                                            # authenticate to Docker Hub
   docker buildx create --name multiarch --use --bootstrap # docker-container driver (once)
   docker buildx build --platform linux/amd64,linux/arm64 \
     -t <dockerhub-user>/dev-workspace:poc --push ../config/dev-workspace
   ```
   Multi-platform builds push the manifest directly (`--push`); a separate
   `docker push` isn't needed, and the image is **not** loaded into your local daemon.
   The repo must be **public** (PoC — no registry credentials in the job); make it
   public in the Docker Hub UI if it was created private.
   (Roadmap: a **private** repo with a Nomad docker `auth` block, or ECR with
   per-project toolchains.)
3. **IBM Verify developers** whose `email` claim matches the `developers` map, able
   to log in through the existing Boundary OIDC method.
4. **Vault platform config applied and Vault unsealed.** The base layer's
   `ssh-secrets-vault` module (SSH CA + signing role + the Nomad JWT/WIF auth
   method) and the Vault credential store must be live, and **Vault must be
   unsealed** — the workspace `sshd` trusts the Vault SSH CA and Boundary signs each
   session's cert through Vault, so a sealed Vault breaks login. Re-unseal after a
   reboot (see `infra/README.md`). No developer SSH keypairs are generated: SSH auth
   is entirely **JIT Vault-signed certs injected by Boundary** (no
   `ssh_public_key` in `terraform.tfvars`, no `IdentityFile` on connect).

## Apply

```bash
cd infra/workspace
cp terraform.tfvars.example terraform.tfvars   # fill in (gitignored — has the admin password)
terraform init
terraform apply
```

Populate the connection inputs from the base outputs:
```bash
terraform -chdir=../infra output -raw nomad_addr
terraform -chdir=../infra output -raw boundary_addr
terraform -chdir=../infra output -raw admin_auth_method_id
terraform -chdir=../infra output -raw admin_login_name
terraform -chdir=../infra output -raw admin_password
terraform -chdir=../infra output -raw boundary_oidc_auth_method_id
terraform -chdir=../infra output -raw project_scope_id
terraform -chdir=../infra output -raw vault_credential_store_id
```

`workspace_host_address` is **not** a base output — it's the node's **private IP**,
where Nomad's docker driver publishes the workspace SSH host ports (see the
troubleshooting note below). Get it from the node:
```bash
ssh <node> hostname -I        # e.g. 10.220.10.196 — the first address
```

> First time only it's a two-step flow (base, then this layer) because the Nomad
> token file and project scope must exist first. Re-applies of this layer are
> single-step.

## Verify the Boundary-brokered workspace SSH

End-to-end, this is the developer demo. Both connect paths are **live-verified (2026-06-02)**: the CLI
`boundary connect ssh` (cert injection) below, and VSCode Remote-SSH / plain `ssh <alias>` via the
Boundary Client Agent + transparent sessions (documented in the next section).

**1. SSO-authenticate to Boundary as the developer** (the same IBM Verify login):
```bash
eval "$(terraform -chdir=../infra output -raw boundary_oidc_login_command)"   # browser OIDC flow
# (this stores a Boundary token for the authenticated developer, e.g. alice)
```
> The OIDC method sets `prompts = ["login"]`, so IBM Verify **forces a fresh
> login every time** — enter the right developer's credentials here. No
> `boundary logout` needed to switch identities (and it errors against the
> self-signed controller cert anyway). (`select_account` alone does NOT force
> re-auth — Verify reuses the existing session.)

**2. Connect with `boundary connect ssh`** (routes through the co-located worker
proxy on 9202 — nothing is exposed on the NLB). Boundary signs a fresh Vault cert
and injects it; there is **no local key and no `-i`**:
```bash
boundary connect ssh -tls-insecure \
  -target-id "$(terraform output -json workspace_target_ids | jq -r '."alice/main"')" \
  -- whoami        # → dev  (proves cert injection logs you in)
```
This is the **verified** connect path today. The brokered cert's `key_id` is the
authenticated developer's email — confirm in the workspace `sshd` stderr:
`nomad alloc logs -stderr -namespace <project> -task workspace <alloc>`.

> Because each new SSH connection re-invokes Boundary and gets a **fresh** cert, the
> 5m cert TTL only covers the handshake while the target's `session_max_seconds`
> (8h) governs session length.

### Verification gates

1. **Static:** `terraform fmt -recursive -check && terraform validate` here and in `../`.
2. **Image:** pushed and public — `docker pull <dockerhub-user>/dev-workspace:poc`
   succeeds from a logged-out client; on the node `sudo docker images | grep
   dev-workspace` shows it after the job pulls it.
3. **Namespace:** `nomad namespace list` shows the project namespace.
4. **Job healthy:** `nomad job status -namespace <project> ws-alice-main` is
   running; `nomad alloc logs <alloc>` shows `Server listening on 0.0.0.0 port 22`.
5. **Cert-only sshd:** exec into the container — `/etc/ssh/trusted_ca.pub` is present,
   `grep -E '^(TrustedUserCAKeys|AuthorizedKeysFile)' /etc/ssh/sshd_config` shows the
   CA path **and** `AuthorizedKeysFile none`, and `/home/dev/.ssh/authorized_keys` is
   **absent** (the entrypoint `rm`s any pre-JIT leftover). **Verified 2026-06-02.**
6. **Persistence (stop/start):** connect, `touch /home/dev/persist-test`, then
   `nomad job stop ws-alice-main` + `terraform apply` (re-run); reconnect → the file
   is still there. (Survives stop/start + reboot, **not** instance replacement.)
7. **Boundary positive:** as alice, the `boundary connect ssh` above logs in with
   **no local key**; the cert `key_id` = alice's email in the `sshd` stderr log.
8. **Boundary NEGATIVE (required):** SSO-authenticate as **bob** (the forced login
   prompt lets you enter bob's credentials — see step 1), then `boundary connect ssh
   -target-id <alice's target>` is **DENIED** — bob's managed group has no grant on
   alice's target. **Verified 2026-06-02.**
9. **Reachability negative:** on the node `ss -tlnp | grep 222` shows the SSH host
   port bound; from off-box `nc -vz <nlb-dns> 2222` **fails** — the only path in is
   Boundary.

## VSCode Remote-SSH via transparent sessions

With the **Boundary Client Agent** the developer skips `boundary connect` entirely:
the agent intercepts DNS for the target's **alias** and brokers + injects the cert
transparently, so `ssh <alias>` (and VSCode Remote-SSH to a normal `Host`) just works.
The `boundary connect ssh` flow above remains the no-Client-Agent fallback.

Per-laptop, one-time (the developer's action, like the OIDC login):

1. **Install the matching Client Agent + Desktop + CLI** (macOS or Windows only). Fully
   **uninstall** any existing Boundary Desktop/CLI first, then install the bundled
   installer from the releases page (the Client Agent ships with the Desktop installer).
   Confirm it is running: `boundary client-agent status`.
2. **Authenticate** as the right developer via the existing OIDC login:
   ```bash
   eval "$(terraform -chdir=../infra output -raw boundary_oidc_login_command)"
   ```
3. **Find your alias** and add a normal `~/.ssh/config` host entry — **no `ProxyCommand`,
   no `IdentityFile`** (Boundary injects the cert). The Client Agent listens on the
   target's port — the workspace's static `ssh_port` — so set `Port` to match; a bare
   `ssh <alias>` would otherwise default to port 22 and reach nothing:
   ```bash
   terraform output -json workspace_aliases   # e.g. "ravi/main": "main.ravi.project-acme.boundary"
   ```
   ```
   Host main.ravi.project-acme.boundary
     Port 2222          # = this workspace's ssh_port (distinct per workspace on the shared node)
     User dev
     StrictHostKeyChecking no
     UserKnownHostsFile /dev/null
   ```
4. **Connect:** `ssh main.ravi.project-acme.boundary` logs in as `dev` with no key (with
   `Port` set above); or in **VSCode** → Remote-SSH: *Connect to Host…* → the alias →
   opens `/home/dev/project`. A benign `client_global_hostkeys_prove_confirm: ... bad
   signature` line may appear — it is the brokering proxy unable to prove the *upstream*
   host key during OpenSSH key rotation; the session is unaffected.

> **Alias resolution permission.** The Client Agent needs the self-scoped
> `list-resolvable-aliases` grant on the user resource to cache the aliases you can
> reach; the `dev_resolve_aliases` role (in `boundary.tf`) grants it. It is self-scoped,
> so you only ever resolve aliases for targets you can already reach — isolation is
> unchanged. Vault must still be **unsealed** (the worker signs/injects the cert per
> session, same as the CLI path).

## Troubleshooting

**`Connection closed by 127.0.0.1 port <p>` / `boundary connect` opens but SSH
immediately drops.** Nomad's docker driver publishes static host ports on the
node's **primary private IP**, not `127.0.0.1`/`0.0.0.0`. If the Boundary host
address doesn't match, the co-located worker dials a dead address and closes the
session. Confirm on the node:
```bash
sudo ss -tlnp | grep ':2222'      # shows e.g. 10.220.10.196:2222, NOT 127.0.0.1
sudo journalctl -u boundary | grep 'connection refused'   # directDialer dial tcp <addr> refused
```
Fix: set `workspace_host_address` to that private IP and re-apply. (`boundary_host_static`
updates in place — target ids are preserved.) Binding the port on loopback instead
would need a `nomad.hcl` change → instance replace, which this day-2 layer avoids.

**`boundary client-agent status` shows `not standards compliant` / a `certificate ... unknown
authority` error (Client Agent path only).** The Client Agent validates the Boundary controller's
self-signed TLS cert with Go's macOS platform verifier and has **no `-tls-insecure` escape** (unlike the
CLI). That verifier rejects any cert valid for more than **398 days** (golang/go#51991), so a long-lived
self-signed controller cert fails even after you trust it in the login keychain. This is an **infra-side**
fix, not a developer action: the base Boundary/Vault/Nomad certs must be ≤398-day
(`infra/modules/secured-codespace/tls.tf` sets 365 days) and the cert must be trusted on the laptop. After
the cert is regenerated, fully restart the agent (`launchctl kickstart -k gui/$(id -u)/<agent-label>`) so
it rebuilds its TLS root pool.

**VSCode: "Failed to set up dynamic port forwarding" / "TCP port forwarding appears to be disabled".**
The Vault signing role must issue certs with the `permit-port-forwarding` extension — VSCode Remote-SSH
uses an SSH direct-tcpip channel, and `sshd` enforces a cert's extensions on top of the global
`AllowTcpForwarding`. This is fixed in `infra/modules/ssh-secrets-vault/vault.tf`; because Boundary signs
a fresh cert per connection, simply **reconnect** (Remote-SSH: *Kill VS Code Server on Host* then
*Connect to Host* again) to pick up a cert with the extension.
