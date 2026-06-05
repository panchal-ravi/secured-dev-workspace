# Architecture — Secured Remote Dev Workspace

A central, secured remote development environment built on the HashiCorp stack
(**Boundary**, **Nomad**, **Vault**) with **IBM Verify** for SSO. Developers do
**not** install AI coding tools or run dev environments on their laptops. Instead a
dev workspace runs centrally on Nomad, and developers connect their IDE (VSCode
Remote-SSH) through an authenticated, authorized **Boundary** session — holding no
SSH key of their own.

> This document describes the **current** architecture. Keep it in sync — update it
> whenever the architecture changes.

## Why this exists — the security model

The whole architecture is organized around a few security properties. Keep these in
mind; every provisioning step below serves one of them.

- **No code or credentials on the laptop.** The source tree, the build toolchain, and
  the AI coding tools all live in a central container. A lost or compromised laptop
  carries no repo and no long-lived secret.
- **Zero standing SSH keys — just-in-time, short-lived certificates.** The workspace
  `sshd` trusts *only* a Vault SSH certificate authority (`TrustedUserCAKeys`, with
  `AuthorizedKeysFile none` so static keys are inert). Boundary brokers each session
  and injects a freshly Vault-signed certificate valid for **5 minutes**, stamped with
  the developer's email as `key_id` for audit. There is no key to steal, leak, or
  rotate.
- **Boundary is the only way in.** The workspace `sshd` is **never** published on the
  load balancer. The only path to it is Boundary's worker proxy. Off-box, the SSH port
  is unreachable.
- **Identity-bound, least-privilege authorization.** Developers authenticate with
  **IBM Verify SSO**. A per-developer Boundary managed group (matched on the
  `/token/email` claim) maps to a role granting `authorize-session` on **only that
  developer's** workspace target — proven by a negative-isolation test (a second
  developer is denied the first's target).
- **Tenant isolation by construction.** Each project gets its own **Vault path**
  (`ssh/<project>`), its own **Boundary project scope**, and its own **Nomad
  namespace**. Cross-project access is structurally impossible, not just policy-gated.
- **Workload identity, not shared secrets.** Nomad fetches the project's SSH CA public
  key from Vault over **workload-identity federation** (the `jwt-nomad` auth method) —
  no static Vault token is baked into a job. The Boundary credential store authenticates
  with a dedicated **least-privilege periodic token**, never the root token.
- **Ephemeral git push credential — no static PAT.** Git comes pre-configured for the
  logged-in developer, and the push credential is a **short-lived (1h, non-renewable)
  GitHub App installation token** minted on demand by Vault (the external GitHub secrets
  plugin) from a **pre-scoped, per-project permission set** (`contents:write`), reached
  over the same WIF path. consul-template re-mints it before expiry; it is rendered only
  to the task's **tmpfs**, never to the persistent `/home/dev` volume, and the App private
  key lives only in Vault. Commits are authored by the developer; the push is the App bot.
- **Locked ingress.** Every load-balancer listener and direct SSH is restricted to the
  `/32` of the machine running Terraform, auto-detected at apply time.
- **Defense in depth.** A namespace-scoped Nomad ACL policy + binding rule backstops the
  design even though developers never receive a Nomad token.

## Architecture

```
IBM Verify (SSO)
      │  authenticate + authorize (group → role)
      ▼
Developer client (VSCode Remote-SSH · ssh · JetBrains)
      │  Boundary transparent session  (Client Agent / boundary connect ssh)
      ▼
Boundary worker proxy ──────────────► Dev workspace (Nomad job, Docker container)
      │  injects 5-min Vault-signed cert         sshd trusts only the Vault SSH CA
      ▼                                          /home/dev on a persistent host volume
Vault  ── signs SSH cert per session (key_id = developer email)
       ── SSH CA per project (ssh/<project>), reached by Nomad over WIF
```

Everything runs on a single all-in-one EC2 node (`ap-southeast-1`): Boundary
controller+worker (local PostgreSQL, AEAD KMS), a combined Nomad server+client
(ACLs + TLS), and a single-node Vault (file storage, self-signed TLS). The Boundary
API (`9200`), Boundary worker proxy (`9202`), Nomad (`4646`) and Vault (`8200`) are
reached through a public NLB; the workspace SSH port is **not**.

## Component roles

Each product in the stack owns one part of the security model:

- **Boundary — identity-aware access broker.** The single, authenticated entry point to
  every workspace. It performs authorization (per-developer managed group → role →
  `authorize-session` on exactly one target), brokers the session through its worker
  proxy, and injects the session credential — so the workspace `sshd` is never network-
  reachable and the developer never learns a host address or holds a key. Target aliases
  + the Client Agent make this transparent to VSCode Remote-SSH.

- **Vault — credential authority / PKI.** The SSH certificate authority. It signs a
  fresh, 5-minute, per-session SSH certificate (`key_id` = developer email, for audit)
  and exposes a per-project CA (`ssh/<project>`) so credentials never cross project
  boundaries. It is **also the git push credential authority**: the external GitHub
  secrets plugin mints short-lived (1h) GitHub App installation tokens from a per-project,
  pre-scoped permission set, so no static PAT is stored anywhere and the App private key
  never leaves Vault. Nomad reaches Vault over **workload-identity federation** (no static
  token) for both the CA key and the git token, and Boundary's credential store uses a
  dedicated least-privilege periodic token rather than the root token.

- **Nomad — workload scheduler / isolation boundary.** Runs each developer workspace as
  a managed container with a persistent host volume (work survives stop/start + reboot),
  and partitions tenants by **namespace** (one per project) backstopped by a
  namespace-scoped ACL. It pulls each project's CA public key from Vault at launch via
  workload identity, so the trust chain is established without embedding secrets.

- **IBM Verify — identity provider.** The SSO source of truth. Both Boundary and Nomad
  trust it via OIDC; per-developer authorization is derived from the `/token/email`
  claim (also the certificate `key_id`), so access is always bound to a named person.

The chain in one line: **IBM Verify** says *who you are*, **Boundary** decides *whether
you may connect and brokers it*, **Vault** mints *the short-lived credential*, and
**Nomad** runs *the workspace you reach*.

## Three tiers, three personas

The Terraform is split into three independently-applied tiers under `terraform/`,
each owned by a different persona and applied in order. This separation is the
operational form of least privilege — the platform team holds the privileged tokens,
project teams onboard their own projects, and developers only ever reach their own
workspace.

| Tier | Path | Persona | Cadence | Creates |
|------|------|---------|---------|---------|
| **Platform** | [`terraform/infra/`](terraform/infra/) | Platform / infra team | **Once** | Boundary/Nomad/Vault clusters, IBM Verify OIDC + SSO, Boundary **org** scope, shared Nomad↔Vault WIF auth method, Vault KV mount |
| **Project** | [`terraform/project/`](terraform/project/) | Project team / platform operator | **Per project** | Boundary project **scope**, Nomad **namespace**, per-project Vault SSH CA + signing role, Boundary cred store + SSH credential library, per-project WIF role, namespace ACL, job templates in Vault KV |
| **Developer** | [`terraform/workspace/`](terraform/workspace/) | Platform operator | **Per workspace** | Workspace container (Nomad job + persistent host volume), Boundary target/alias + per-developer managed group/role, `~/.ssh/config` snippet |

Per-instance state is kept with `terraform workspace` (one per project / per
developer-workspace); the lower roots read the upstream tier's outputs via
`terraform_remote_state`, so there is **no token or address copying** between tiers.

## Provisioning sequence (by persona)

Apply order is strict: **platform → project → developer**. **Vault must be unsealed**
for the project and developer applies and for every connect (Boundary signs each
session cert through Vault).

### 1. Platform team — once

Stand up the clusters, SSO, and shared trust anchors. Full instructions:
[`terraform/infra/README.md`](terraform/infra/README.md).

```bash
cd terraform/infra
# Add Boundary/Nomad/Vault Enterprise licenses under config/ (gitignored).
# Build the base AMI (Packer) — see the platform README.
cp terraform.tfvars.example terraform.tfvars     # base values + IBM Verify tenant/API creds
terraform init
terraform apply -target=module.secured_codespace  # bring the base node up first
terraform apply                                    # IBM Verify OIDC + WIF auth method + KV mount
```

Outputs the lower tiers consume: `boundary_addr`, `nomad_addr`, `vault_addr`,
`org_scope_id`, admin login/password + `admin_auth_method_id`, the OIDC method IDs, the
WIF backend path, and the KV mount path. Verify SSO login works for both Boundary and
Nomad, Vault is unsealed with the KV mount + `jwt-nomad` present, and the Boundary org
scope exists (no project scope yet).

> IBM Verify prerequisites (a SaaS tenant, a bootstrap API client, and the
> admin/readonly groups) are one-time and described in the platform README. The
> `ibm_verify_*` variables have no defaults and must be set before any apply.

### 2. Project team — per project

Onboard a project: its own Boundary scope, Nomad namespace, Vault SSH CA, credential
store/library, WIF role, namespace ACL, and job templates. Full instructions:
[`terraform/infra/README.md` → Project onboarding](terraform/infra/README.md#project-onboarding-terraformproject).

```bash
cd terraform/project
terraform init
cp terraform.tfvars.example project-acme.tfvars   # set project_name + developers_group_name
terraform workspace new project-acme               # one Terraform workspace per project
terraform apply -var-file=project-acme.tfvars
```

The project tfvars also carry the project's **GitHub App** (App ID, installation ID,
private-key PEM) — a one-time manual prerequisite per project (create the App with
**Contents: Read & write**, install it on the repos). This tier configures the
`github/<project>` token broker + a pre-scoped `dev-workspace` permission set from them.

Each project is fully isolated — separate Vault path (`ssh/project-acme`), Boundary
scope, Nomad namespace, and GitHub App. Onboard another by repeating with a new workspace
+ tfvars. The job template is readable at `secret/projects/<project>/job-templates/<name>`
and the outputs (`project_scope_id`, `namespace`, `credential_library_id`, `ssh_ca_path`,
`wif_role`, `github_token_path`, …) feed the developer tier.

### 3. Platform operator — per developer workspace

Spin up one developer's workspace from a selected project job template, and create the
Boundary target + per-developer authorization. Full instructions and verification gates:
[`terraform/workspace/README.md`](terraform/workspace/README.md).

```bash
cd terraform/workspace
# Build + push the dev-workspace image (multi-arch) to a public registry — see the workspace README.
terraform init
cp terraform.tfvars.example ravi-main.tfvars       # developer_handle/email, project_name, ssh_port, git_repo_url, image
terraform workspace new ravi-main                  # one Terraform workspace per developer-workspace
terraform apply -var-file=ravi-main.tfvars -out=ravi-main.tfplan   # review the saved plan, then apply
```

This deploys the workspace container (persistent `/home/dev` host volume, first-boot
clone of the project repo), the Boundary target/alias, the per-developer managed
group + role, and a ready-to-paste `~/.ssh/config` snippet (`ssh_config_path` output).

### 4. Developer — connect

The developer authenticates with **IBM Verify SSO** and connects with **no SSH key** —
Boundary brokers the session and injects a short-lived Vault-signed certificate.

```bash
# SSO-authenticate to Boundary (browser OIDC flow), then either:
boundary connect ssh -target-id <target-id> -- whoami        # CLI fallback
# ...or VSCode Remote-SSH over the Boundary Client Agent (transparent sessions) —
# plain `ssh <ws>.<dev>.<project>.boundary`, no ProxyCommand. See the workspace README.
```

The negative-isolation gate (a second developer is **denied** the first's target) and
the off-box reachability gate (the SSH port is unreachable except via Boundary) are
documented and verified in [`terraform/workspace/README.md`](terraform/workspace/README.md).

## Teardown (destroy the environment)

Destroy in the **reverse** of the apply order — **developer → project → platform** — so
the Boundary/Nomad/Vault control planes each tier talks to stay up until the platform node
is torn down last. **Vault must be unsealed** for the developer and project destroys: they
revoke Boundary/Nomad/Vault resources through those APIs, and a sealed Vault makes them
hang or error. `terraform destroy` prints a plan and prompts before deleting — review it
and confirm (no `-auto-approve` on the shared clusters).

### 1. Developer workspaces — each Terraform workspace

```bash
cd terraform/workspace
terraform workspace list                          # every developer-workspace
terraform workspace select ravi-main
terraform destroy -var-file=ravi-main.tfvars      # review the plan, confirm
# repeat for each remaining workspace (pravi-main, …)
```

Removes the workspace container **and its host volume — all `/home/dev` data is lost** —
plus the Boundary target/alias and the per-developer managed group/role.

### 2. Projects — each project Terraform workspace

```bash
cd ../project
terraform workspace select project-acme
terraform destroy -var-file=project-acme.tfvars   # review, confirm
# repeat for each project
```

Removes the Boundary project scope, Nomad namespace, Vault SSH CA, credential
store/library, WIF role, namespace ACL, and the `github/<project>` token broker. The
project's **GitHub App is external** (not Terraform-managed) — it survives; delete it in
GitHub manually only if you are decommissioning the project for good.

### 3. Platform — last

```bash
cd ../infra
terraform destroy                                 # review, confirm
```

Tears down the EC2 node and the Boundary/Nomad/Vault clusters, and the IBM Verify OIDC
**apps** (recreated on the next apply). The IBM Verify **tenant** itself is external and
untouched.

> **Order matters.** Do **not** destroy the platform tier while any project or developer
> state still exists — those tiers' providers read the platform's addresses and
> credentials from its state, so their destroys would fail with no control plane left to
> call. Always empty the lower tiers first.

> **State cleanup (optional).** Once a tier's resources are gone you can drop the now-empty
> per-instance state: `terraform workspace select default && terraform workspace delete <name>`.
> The gitignored `*.tfvars` and any `*.tfplan` files can then be removed too.

## Repository layout

```
terraform/
├── infra/          # PLATFORM tier (once) — clusters, IBM Verify SSO, org scope, WIF, KV,
│   │               #   GitHub secrets-plugin registration (vault-github-plugin.tf)
│   ├── ami/        # Packer base image (Boundary/Nomad/Vault/Consul Enterprise binaries
│   │               #   + the baked GitHub secrets plugin)
│   ├── config/     # cluster HCL, systemd units, licenses, dev-workspace Dockerfile
│   └── modules/    # secured-codespace · identity · nomad-vault-wif
├── project/        # PROJECT tier (per project) — FLAT root (no child module);
│   │               #   github.tf = per-project GitHub App token broker
│   └── templates/  #   Nomad job templates (raw HCL → Vault KV)
└── workspace/      # DEVELOPER tier (per workspace) — FLAT root (no child module)
```

> The `project/` and `workspace/` tiers are **flat** roots: each was a single-instance
> module wrapper, so the resources now live directly in the tier root (per-instance
> isolation comes from `terraform workspace`, not the module boundary). The `infra/` tier
> keeps its modules — they are genuinely distinct subsystems.

## Prerequisites

- AWS credentials in the environment (region defaults to `ap-southeast-1`).
- Packer ≥ 1.9, Terraform ≥ 1.9 (the developer tier renders job templates with
  `templatestring()`).
- Boundary, Nomad **and** Vault Enterprise licenses (the AMI bakes `+ent` binaries).
- An IBM Verify SaaS tenant + bootstrap API client (see the platform README).
- A **GitHub App per project** (Contents: Read & write), installed on the project's
  repos — supplies the workspace git push credential (see the project onboarding section).

## Operational guardrails

- **Never** commit `*.tfvars`, plan files, or `generated/` artifacts — they embed the
  Boundary admin password and Vault/Nomad tokens. All are gitignored.
- On the shared Boundary/Nomad/Vault clusters, prefer a **saved, reviewed plan**
  (`terraform apply -out=<file>.tfplan`) over `-auto-approve`; delete the plan file
  afterward.
- **Re-unseal Vault after any node reboot** before running Terraform or connecting —
  the demo uses Shamir unseal, so Vault comes back sealed. See the platform README.
- The workspace host volume lives on the node root EBS: data survives **stop/start +
  reboot** but **not** an instance replacement.
```