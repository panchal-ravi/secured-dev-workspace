# Boundary Enterprise — all-in-one on AWS

Provisions **HashiCorp Boundary Enterprise** as a single all-in-one node on AWS:

1. A **Packer** base image (`ami/base_image/`) that bakes the Boundary, Consul,
   Nomad and Vault Enterprise binaries onto the org base AMI.
2. A **Terraform** module (`modules/secured-codespace/`) that launches one EC2
   instance from that AMI running, on the same host: the Boundary **controller +
   worker** (backed by a **local PostgreSQL**, static **AEAD KMS keys**) and a
   single combined **Nomad server + client** agent (TLS + ACLs enabled).

The Boundary controller API (`9200`), Boundary worker proxy (`9202`), the Nomad
HTTP API/UI (`4646`) and the Vault API/UI (`8200`) are all reached through a public **Network Load Balancer**.
SSH is direct to the instance. **All ingress — every NLB listener and SSH — is
locked to the public IP of the machine running Terraform (`/32`)**, auto-detected
via `https://checkip.amazonaws.com`.

## Layout

```
infra/
├── ami/base_image/            # Packer: builds <owner>-boundary-enterprise-* AMI
├── config/                    # Boundary HCL template, systemd unit, license
├── modules/secured-codespace/ # VPC, NLB, SGs, TLS, EC2, bootstrap (+ Vault server)
├── modules/identity/          # IBM Verify OIDC SSO for Boundary + Nomad (Phase 6)
├── modules/credential-store-vault/ # Boundary Vault credential store + least-privilege token
├── modules/ssh-secrets-vault/ # Vault SSH CA + signing role + Nomad↔Vault WIF (JIT workspace SSH certs)
├── config/dev-workspace/      # Dockerfile for the dev-workspace:poc image (Phase 2)
├── workspace/                 # day-2 root: namespaces, persistent workspaces, per-dev Boundary isolation
├── providers.tf  variables.tf  main.tf  outputs.tf  terraform.tfvars
└── generated/                 # runtime artifacts (SSH key, init JSON) — gitignored
```

Everything lives in a **single Terraform state**: the base node (`modules/secured-codespace`,
which now also runs Vault), the IBM Verify SSO layer (`modules/identity`), the Boundary Vault
credential store (`modules/credential-store-vault`) and the Vault SSH CA + Nomad↔Vault workload-identity
federation (`modules/ssh-secrets-vault`, which signs the JIT workspace SSH certs). All module calls are in
`main.tf`; all provider configs are in `providers.tf`.

## Modules

What each module provisions and the order it applies in:

- **`modules/secured-codespace`** *(base — applied first)* — the all-in-one node and everything it
  depends on: VPC, subnets, security groups (locked to your `/32`), the public NLB, the self-signed TLS
  certs, and the EC2 instance whose cloud-init bootstraps the **Boundary controller+worker** (local
  PostgreSQL + AEAD KMS), the combined **Nomad server+client** (ACLs + TLS), and the single-node
  **Vault** server (init + unseal). Emits all the connection outputs (`*_addr`, admin creds, scope IDs,
  tokens) the day-2 modules and providers consume.
- **`modules/identity`** *(day-2 — IBM Verify SSO, Phase 6)* — creates two OIDC apps in the IBM Verify
  SaaS tenant via REST and wires **Boundary and Nomad** to trust them, mapping Verify group membership
  → admin / readonly. Additive: the Boundary password admin and Nomad management token remain as
  break-glass logins.
- **`modules/credential-store-vault`** *(day-2)* — registers Vault as a **Boundary Vault credential
  store** in the project scope, authenticated with a dedicated least-privilege periodic token (not the
  root token). The workspace targets attach a credential library to this store.
- **`modules/ssh-secrets-vault`** *(day-2)* — turns Vault into the **SSH certificate authority** for the
  dev workspaces: the `ssh-client-signer` CA mount + `dev-workspace` signing role, plus the
  **Nomad↔Vault workload-identity** (`jwt-nomad`) auth method. Boundary signs a short-lived SSH cert per
  session; the workspace `sshd` trusts only this CA, so developers hold no key.
- **`workspace/`** *(separate day-2 root — not part of this state)* — the per-developer secured
  workspaces (Nomad namespaces, persistent Docker containers, Boundary targets + per-dev OIDC isolation).
  Applied from its own root after the base is up; see [Phase 2: dev workspace](#phase-2-dev-workspace-infraworkspace).

## Prerequisites

- AWS credentials in the environment (region defaults to `ap-southeast-1`).
- Packer >= 1.9, Terraform >= 1.7.
- A Boundary Enterprise license **and** a Nomad Enterprise license (the AMI bakes
  `+ent` binaries, which require a license to start).

## Steps

1. **Add your licenses** — Boundary at `config/boundary_license.hclic`, Nomad at
   `config/nomad_license.hclic` and Vault at `config/vault_license.hclic` (replace the placeholders).
   All are gitignored. Vault Enterprise will not start without its license.

2. **Build the AMI:**
   ```bash
   cd ami/base_image
   packer init .
   packer build -var-file=variables.pkrvars.hcl .
   ```
   Produces `<owner>-boundary-enterprise-<timestamp>`, tagged with the four versions.

3. **Configure variables:**
   ```bash
   cd ..
   cp terraform.tfvars.example terraform.tfvars
   ```
   Set the base values and the three IBM Verify values (`ibm_verify_tenant`,
   `ibm_verify_api_client_id`, `ibm_verify_api_client_secret`). The `ibm_verify_*`
   variables have **no defaults**, so they must be set before any `terraform`
   plan/apply will run — see [IBM Verify OIDC SSO](#ibm-verify-oidc-sso-phase-6)
   below for what they are and how to obtain them. (`terraform.tfvars` is
   gitignored — never commit secrets.)

4. **Provision:**
   ```bash
   terraform init
   # Single state: the day-2 providers (boundary/nomad/vault/restapi) connect to the
   # base over the NLB, so on a fresh stack bring the base up first...
   terraform apply -target=module.secured_codespace
   # ...then a plain apply provisions everything that layers on the now-running base:
   #   module.identity              — IBM Verify OIDC apps + Boundary/Nomad SSO wiring
   #   module.credential_store_vault — Boundary Vault credential store + scoped token
   #   module.ssh_secrets_vault      — Vault SSH CA + signing role + Nomad↔Vault WIF
   terraform apply
   ```
   The first apply finds the just-built AMI via the `<owner>-boundary-enterprise-*`
   filter and waits for cloud-init (Postgres + `boundary database init` + the
   recovery-KMS setup that creates the admin, org/project scopes, password auth
   method and admin role; then the Nomad agent + `nomad acl bootstrap`; then
   `vault operator init`/unseal with the `vault{}` JWT-auth stanza baked into
   `nomad.hcl`), copying the generated IDs to `generated/boundary-setup.json`, the
   Nomad management token to `generated/nomad-setup.json`, and the Vault root token +
   unseal keys to `generated/vault-setup.json`. The second apply fetches an IBM Verify
   token and creates the two OIDC apps (wiring Boundary + Nomad to trust them — see
   the SSO section below), registers the Boundary Vault credential store, and stands
   up the Vault SSH CA + signing role + the Nomad↔Vault workload-identity auth method
   that together issue the JIT workspace SSH certificates.

   > **Re-applying onto an existing instance:** `aws_instance.this` has
   > `lifecycle { ignore_changes = all }`, so changes to the bootstrap/config are not
   > pushed by a plain `apply`. To roll them onto a running node, replace it:
   > `terraform apply -replace='module.secured_codespace.aws_instance.this'`.

5. **Retrieve admin credentials and scope IDs:**
   ```bash
   terraform output boundary_addr
   terraform output admin_auth_method_id
   terraform output admin_login_name
   terraform output -raw admin_password
   terraform output org_scope_id
   terraform output project_scope_id
   ```
   The login name, password and scope/project names are whatever you set in
   `terraform.tfvars` (defaults: `admin` / `Password123!` / `primary-org` /
   `primary-project`). The auth-method and scope **IDs** are server-generated and read
   back from `generated/boundary-setup.json`.

6. **Connect** (self-signed API cert, so skip TLS verification):
   ```bash
   boundary authenticate password \
     -addr "$(terraform output -raw boundary_addr)" \
     -auth-method-id "$(terraform output -raw admin_auth_method_id)" \
     -login-name "$(terraform output -raw admin_login_name)" \
     -tls-insecure
   ```
   (Enter the configured `boundary_admin_password` when prompted.)

7. **Connect to Nomad** (self-signed API cert, so skip TLS verification):
   ```bash
   export NOMAD_ADDR="$(terraform output -raw nomad_addr)"
   export NOMAD_TOKEN="$(terraform output -raw nomad_management_token)"
   export NOMAD_SKIP_VERIFY=true
   nomad server members        # the node should be "alive"
   nomad node status           # the client should be "ready"
   ```
   The web UI is at `$(terraform output -raw nomad_ui_addr)` (paste the management
   token under **ACL → Sign in with a token**).

8. **Connect to Vault** (self-signed API cert, so skip TLS verification):
   ```bash
   export VAULT_ADDR="$(terraform output -raw vault_addr)"
   export VAULT_SKIP_VERIFY=true
   export VAULT_TOKEN="$(terraform output -raw vault_root_token)"
   vault status        # Initialized=true, Sealed=false
   vault token lookup  # the root token
   ```
   The web UI is at `$(terraform output -raw vault_addr)/ui`.

## Vault server + Boundary credential store

`modules/secured-codespace` runs a single-node **Vault** (file storage, self-signed
TLS) on the all-in-one node, reached at `:8200` through the NLB. The bootstrap
runs `vault operator init` (Shamir, 1 share / 1 threshold for this demo) and
unseals it; the **root token** and **unseal keys** are scp'd back to
`generated/vault-setup.json` and surfaced as the sensitive outputs
`vault_root_token` / `vault_unseal_keys`. `modules/credential-store-vault`
(called from `main.tf`) then registers Vault as a **Boundary Vault credential
store** in the project scope, authenticated with a **dedicated least-privilege
periodic token** (policy + token in that module — *not* the root token). This is
Phase 4 groundwork; the Phase 2 workspace targets attach a credential library to this store.

`modules/ssh-secrets-vault` then turns Vault into the **SSH certificate authority** for the dev
workspaces: a `ssh` secrets mount in signing (CA) mode, a `dev-workspace` signing role
(`permit-pty` + `permit-port-forwarding`, 5m/10m TTL, `key_id` = the developer's email), and the
**Nomad↔Vault workload-identity (WIF)** auth method (`jwt-nomad`) that lets a workspace task fetch the CA
public key over its Nomad-signed identity JWT. The workspace `sshd` trusts only this CA
(`TrustedUserCAKeys`) and Boundary injects a freshly-signed cert per session — the developer holds no key.
The CA public key and sign path surface as the `ssh_ca_public_key` / `ssh_sign_path` outputs. See
`docs/specs/phase-2-jit-vault-ssh-certs.md`. The base `nomad.hcl` carries the `vault{}` stanza for WIF, so
this works on a fresh build; rolling it onto an already-running node needs the in-place `nomad.hcl` update
+ `systemctl restart nomad` described in that spec (no instance replace).

> **Re-unseal after a reboot.** With Shamir unseal, Vault comes back **sealed**
> whenever the node restarts. Because the root `vault` provider is configured at
> the root, **every** `terraform plan`/`apply` needs Vault unsealed — re-unseal it
> first:
> ```bash
> VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true \
>   vault operator unseal "$(terraform output -json vault_unseal_keys | jq -r '.[0]')"
> ```

> **Enabling Vault on an already-running stack.** Vault's config lives in the
> node's bootstrap/user_data, which is `lifecycle { ignore_changes = all }`, so a
> plain `apply` will not roll it onto a running instance. Replace the instance:
> `terraform apply -replace='module.secured_codespace.aws_instance.this'`, then a
> plain `terraform apply` to configure the credential store. The replace rebuilds
> Boundary/Nomad and re-runs the SSO wiring.

## IBM Verify OIDC SSO (Phase 6)

`modules/identity` (called from `main.tf`) creates two OIDC applications in an
**IBM Verify SaaS** tenant via its REST API and configures Boundary **and** Nomad
to trust them, mapping Verify **group membership** to admin / readonly access. It
is **additive** — the Boundary password admin and the Nomad management token stay
as break-glass logins.

Prerequisites (manual, one-time):

1. An IBM Verify SaaS tenant.
2. A **bootstrap API client** (Verify console → Security → API access) with
   entitlements `manageAppAccessAdmin` **and** `readAppConfigAndClientSecret`.
3. Two Verify groups (`secured-codespace-admins`, `secured-codespace-readonly`)
   with members assigned.
4. The IBM Verify variables set in `terraform.tfvars` (see
   `terraform.tfvars.example`): `ibm_verify_tenant`, `ibm_verify_api_client_id`,
   `ibm_verify_api_client_secret`.

> **Single-state tradeoff:** because the `boundary`/`nomad`/`restapi` providers
> are configured from the base module's outputs and a live Verify token,
> **every** `terraform plan`/`apply` now fetches a Verify bootstrap token and
> connects to Boundary + Nomad. So the `ibm_verify_*` variables must be populated
> and **the base instance must already be applied and running** before any plan
> succeeds — which is why the provisioning flow above targets
> `module.secured_codespace` first, then runs a plain `terraform apply`.

After applying, log in via SSO:

```bash
eval "$(terraform output -raw boundary_oidc_login_command)"   # browser OIDC flow
eval "$(terraform output -raw nomad_oidc_login_command)"      # CLI loopback flow
```

## Phase 2: dev workspace (`infra/workspace/`)

A **separate day-2 Terraform root** deploys the secured coding workspaces on top of the running base
stack. It talks only to Nomad + Boundary over the NLB, so it **re-applies without an instance replace**.
Driven by `projects` and `developers` variables, it creates, per developer-workspace: a Nomad
**namespace** (per project), a **persistent** Docker workspace container (sshd + build tools) on a
**dynamic host volume** (`/home/dev` survives stop/start + reboot), a first-boot clone of the project's
**public** repo, and a **Boundary ssh target** plus a **per-developer OIDC managed group + role** so each
developer can connect to **only their own** workspace. SSH auth is **JIT Vault-signed certs injected by
Boundary** — the workspace `sshd` trusts only a Vault SSH CA (`TrustedUserCAKeys` + `AuthorizedKeysFile
none`) and the developer holds no key (see `docs/specs/phase-2-jit-vault-ssh-certs.md`). Identity is the
existing IBM Verify SSO (matched on the `/token/email` claim, also the cert `key_id`) — no password accounts.

Build + push the image to a **public Docker Hub** repo (multi-arch so it runs on the amd64 node from any
build host), then apply (**Vault must be unsealed** — Boundary signs the session cert through Vault):

```bash
docker login
docker buildx create --name multiarch --use --bootstrap          # once
docker buildx build --platform linux/amd64,linux/arm64 \
  -t <dockerhub-user>/dev-workspace:poc --push config/dev-workspace

cd workspace
cp terraform.tfvars.example terraform.tfvars   # fill from `terraform -chdir=../ output ...` (gitignored)
terraform init && terraform apply
```

See `infra/workspace/README.md` for the full variable wiring and verification gates. Scope is a
single-node PoC (Docker isolation, public-repo clone); durable CSI/EBS storage, private-repo clone via
Vault, multi-node, microVM, and the developer portal are roadmap (`docs/PLAN.md`).

> The host volume lives on the node root EBS — workspace data survives **stop/start + reboot** but
> **not** an instance replacement.

## Verify the Boundary-brokered workspace SSH

The end-to-end developer demo (run from `infra/workspace/` after applying). Two connect paths are
**live-verified (2026-06-02)**: VSCode Remote-SSH (and plain `ssh <alias>`) via the **Boundary Client
Agent + transparent sessions**, and the CLI `boundary connect ssh` as the no-Client-Agent fallback. The
CLI path is shown below; the transparent-session / VSCode steps are in `infra/workspace/README.md`
("VSCode Remote-SSH via transparent sessions").

1. **SSO-authenticate to Boundary as the developer** (same IBM Verify login):
   ```bash
   eval "$(terraform -chdir=../ output -raw boundary_oidc_login_command)"   # browser OIDC flow as e.g. alice
   ```
2. **Connect with `boundary connect ssh`** (routes via the co-located worker proxy on 9202 — nothing is
   on the NLB). Boundary signs a fresh Vault cert and injects it — **no local key**:
   ```bash
   boundary connect ssh -tls-insecure \
     -target-id "$(terraform output -json workspace_target_ids | jq -r '."alice/main"')" \
     -- whoami        # → dev
   ```
   The cert `key_id` is alice's email (in the workspace `sshd` stderr). For **VSCode Remote-SSH** over
   these injected targets, use the **Boundary Client Agent + transparent sessions** path documented in
   `infra/workspace/README.md` (plain `ssh <alias>` / a normal VSCode `Host` entry — no `ProxyCommand`).
3. **Negative isolation test (required):** SSO-authenticate as **bob** — IBM Verify forces a fresh login
   every time (the OIDC method sets `prompts = ["login"]`), so enter bob's credentials rather than
   reusing alice's session — then `boundary connect ssh` to *alice's* target id is **DENIED** (bob's
   managed group has no grant on alice's target). **Verified 2026-06-02.**
4. **Reachability negative:** on the node `ss -tlnp | grep 222` shows the SSH host port bound, but
   off-box `nc -vz <nlb-dns> 2222` **fails** — Boundary is the only path in.

## Notes

- AEAD keys are generated once by Terraform (`random_bytes`) and held in state, so
  they remain **stable across reboots** — required, since rotating them would orphan
  the DB-encrypted data.
- The admin account, org/project scopes, password auth method and admin role are
  created on the instance during cloud-init via Boundary's **recovery-KMS workflow**
  (no controller login required — it authenticates with the recovery AEAD key). This
  runs locally against `127.0.0.1:9200`, so it does not depend on the NLB being healthy.
  Login name, password and the org/project names come straight from `terraform.tfvars`.
- `generated/` holds the SSH private key, `boundary-setup.json` (server-generated
  scope/auth-method/user/role IDs — no secrets) and `nomad-setup.json` (the Nomad ACL
  bootstrap **management token** — a secret); it is gitignored and both setup files are
  removed on `terraform destroy`. The Boundary admin password is a Terraform variable,
  not a file artifact.
- **Nomad** runs as a single combined `server { bootstrap_expect = 1 }` + `client`
  agent with **ACLs** and **TLS** enabled, standalone (no Consul). It runs as **root**
  so the client's docker/exec drivers work; `nomad acl bootstrap` runs once during
  cloud-init and the management token surfaces via the `nomad_management_token`
  (sensitive) output. The Nomad Enterprise license must be supplied at
  `config/nomad_license.hclic`.
- Scope is a single demo node: no AWS KMS, no RDS, no separate controller/worker
  split, no bastion. The `hashicorp/boundary` provider (targets/host-catalogs) is out
  of scope; the initial scopes/auth-method/admin/role are provisioned via the CLI
  recovery workflow above instead.
- Change the owner, region, instance type, or binary versions in `terraform.tfvars`
  and `ami/base_image/variables.pkrvars.hcl`.
