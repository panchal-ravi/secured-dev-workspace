# Architecture — Secured Remote Dev Workspace

A central, secured remote development environment built on the HashiCorp stack
(**Boundary**, **Nomad**, **Vault**) with **IBM Verify** for SSO. Developers do
**not** install AI coding tools or run dev environments on their laptops. Instead a
dev workspace runs centrally on Nomad, and developers connect their IDE (VSCode
Remote-SSH) through an authenticated, authorized **Boundary** session — holding no
SSH key of their own.

## Security model

The architecture is organized around a small set of security properties. Each component
and provisioning step described below exists to uphold one of them.

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

In the reference deployment, the control plane runs on a single all-in-one EC2 node
(`ap-southeast-1`): Boundary controller+worker (local PostgreSQL, AEAD KMS), a combined
Nomad server+client (ACLs + TLS), and a single-node Vault (file storage, self-signed
TLS). A production deployment scales these onto separate, highly-available clusters per
HashiCorp's reference architectures; the logical model is unchanged. The Boundary API
(`9200`), Boundary worker proxy (`9202`), Nomad (`4646`) and Vault (`8200`) are reached
through a public NLB; the workspace SSH port is not.

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

## Three tiers, three roles

The Terraform is split into three independently-applied tiers under `terraform/`,
each owned by a different role and applied in order. This separation is the
operational form of least privilege — the **Platform Admin** holds the privileged tokens,
**Project Admins** onboard their own projects, and **Developers** only ever provision and
reach their own workspace (self-service through the Developer Portal).

| Tier | Path | Role | Cadence | Creates |
|------|------|------|---------|---------|
| **Platform** | [`terraform/infra/`](terraform/infra/) | Platform Admin | **Once** | Boundary/Nomad/Vault clusters, IBM Verify OIDC + SSO, Boundary **org** scope, shared Nomad↔Vault WIF auth method, Vault KV mount |
| **Project** | [`terraform/project/`](terraform/project/) | Project Admin | **Per project** | Boundary project **scope**, Nomad **namespace**, per-project Vault SSH CA + signing role, Boundary cred store + SSH credential library, per-project WIF role, namespace ACL, job templates in Vault KV |
| **Developer** | [`terraform/workspace/`](terraform/workspace/) | Developer | **Per workspace** | Workspace container (Nomad job + persistent host volume), Boundary target/alias + per-developer managed group/role, `~/.ssh/config` snippet |

Per-instance state is kept with `terraform workspace` (one per project / per
developer-workspace); the lower roots read the upstream tier's outputs via
`terraform_remote_state`, so there is **no token or address copying** between tiers.

## Logical architecture by persona

The architecture composes **cumulatively** across three layers — a platform foundation, an
isolated project slice, and a developer sandbox — each owned by a different role. The diagrams
below are logical views: they show the design pattern rather than any specific deployment
topology, so the number and placement of nodes reflect the production model, not the reference
PoC. Each diagram builds on the one before it and is followed by a table describing the role each
component plays in the security model. In the diagrams, **dashed outlines are trust boundaries**
and **bold-blue notes call out the security property each layer adds**.

### Platform Admin — foundation

The foundation provides identity, the HashiStack security authorities, a Nomad cluster of standard
and GPU worker nodes, and the centralized **AI / MCP gateway**. IBM Verify is the identity provider
both Boundary and Nomad trust; Boundary is the single, identity-aware entry to every workspace;
Vault is the authority for all just-in-time credentials; and the gateway is the one policy point
governing which MCP servers, tools, and LLMs any workload may reach.

![Logical foundation: IBM Verify, Boundary, Vault, the AI / MCP gateway, and a Nomad cluster of standard and GPU worker nodes](diagrams/01-platform-admin.png)

| Component | Role in the security model |
|---|---|
| **IBM Verify (SaaS)** | OIDC/SSO identity provider that both Boundary and Nomad trust; every access is bound to a named person (the `/token/email` claim). |
| **Boundary** | Identity-aware access broker — the org scope + OIDC auth method; brokers each workspace session and injects its credential, so no workspace is directly network-reachable. |
| **Vault** | Credential authority and PKI — the `secret/` KV mount and the `jwt-nomad` workload-identity auth method; mints every short-lived credential for SSH, MCP, Git, and LLM access. |
| **Nomad cluster** | Workload scheduler and isolation boundary — standard and `gpu` node pools that run every workload, including the gateway itself. |
| **AI / MCP Gateway (ContextForge)** | The centralized AI/MCP policy point (Nomad job, namespace `infra`) — governs which MCP servers, tools, and LLMs a workspace may reach. |
| **Network ingress** | A single public load balancer is the only ingress; every listener is restricted to the operator `/32`, and Nomad reaches Vault over workload-identity federation, so no static tokens are baked into jobs. |

### Project Admin — project slice

Each project is carved out as an **isolated slice** on top of the foundation: a Boundary scope, a
Nomad namespace, per-project Vault paths, a project database with its own MCP service, and that
service federated into the gateway as a per-project **virtual MCP server** reachable only with a
scoped client token. Tenant isolation is structural — by scope, namespace, and Vault path prefix —
rather than policy-gated.

![Logical project slice: the foundation plus a per-project Boundary scope, Nomad namespace, Vault paths, database with MCP service, and gateway virtual server](diagrams/02-project-admin.png)

| Component | Role in the security model |
|---|---|
| **Boundary project scope** | Per-project scope + credential store / SSH credential library (a least-privilege periodic token), isolating brokered access to this project. |
| **Nomad namespace** | Per-project namespace (ACL-backstopped) partitioning every workload, so projects cannot schedule into one another. |
| **Per-project Vault paths** | `ssh/<project>` (SSH CA), `database/<project>` (dynamic read-only Postgres role), `github/<project>` (GitHub App token broker), `secret/projects/<project>/*` (KV) — isolation by path. |
| **demo-db + demo-db-mcp** | A Postgres database and a long-running postgres-mcp (SSE) service whose DB credential is a dynamic, auto-rotating Vault read-only role — no static DB password. |
| **Gateway virtual server + scoped token** | demo-db-mcp is registered as a gateway peer and exposed as a per-project virtual MCP server; a scoped client token (written to `secret/projects/<project>/mcp`) reaches only that server. |

### Developer — sandbox

A developer self-services a workspace through the **Developer Portal** (or the equivalent
`terraform/workspace` path), which renders it from the same Vault-KV job template. The coding agent
runs centrally in the sandbox container — not on the laptop — reaching its LLM and only its
project's virtual MCP server through the gateway. Access is a Boundary session carrying a
just-in-time, short-lived Vault-signed SSH certificate, so the developer holds no key and secrets
render only to in-memory tmpfs.

![Logical developer sandbox: the project slice plus the Developer Portal, a workspace sandbox running the coding agent with remote MCP, and brokered just-in-time access](diagrams/03-developer.png)

| Component | Role in the security model |
|---|---|
| **Developer Portal** | Self-service trusted backend (IBM Verify OIDC); shows only the projects the developer's groups grant and provisions the workspace over the Nomad/Boundary/Vault APIs. |
| **Workspace container** | The AI-agentic sandbox — a Nomad job running Claude Code → DeepSeek with a persistent `/home/dev` volume; secrets render only to **tmpfs `/secrets`**, never to the volume. |
| **Boundary target + per-developer group/role** | An email-bound managed group authorizes exactly one workspace target (negative-isolation tested); the workspace `sshd` is never published on the NLB. |
| **JIT Vault SSH certificate** | Boundary injects a fresh 5-minute Vault-signed certificate (`key_id` = developer email) per session — the developer holds no SSH key to steal or rotate. |
| **Remote MCP via the gateway** | Claude connects to the project's virtual MCP server with the scoped token from tmpfs — AI tool/data access is gated centrally by gateway policy, not embedded per workspace. |

## Provisioning sequence (by role)

The tiers apply in a strict order — **platform → project → developer**. Vault is unsealed
for the project and developer applies and for every connection, since Boundary signs each
session certificate through Vault.

### 1. Platform Admin — once

The platform tier stands up the clusters, SSO, and shared trust anchors. Full instructions:
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
WIF backend path, and the KV mount path. After this apply, SSO login works for both Boundary
and Nomad, Vault is unsealed with the KV mount + `jwt-nomad` present, and the Boundary org
scope exists (no project scope yet).

> IBM Verify prerequisites (a SaaS tenant, a bootstrap API client, and the
> admin/readonly groups) are one-time and described in the platform README. The
> `ibm_verify_*` variables have no defaults and are set before any apply.

### 2. Project Admin — per project

The project tier onboards a project — its own Boundary scope, Nomad namespace, Vault SSH CA,
credential store/library, WIF role, namespace ACL, and job templates. Full instructions:
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
scope, Nomad namespace, and GitHub App. Additional projects are onboarded by repeating with a
new workspace + tfvars. The job template is readable at `secret/projects/<project>/job-templates/<name>`
and the outputs (`project_scope_id`, `namespace`, `credential_library_id`, `ssh_ca_path`,
`wif_role`, `github_token_path`, …) feed the developer tier.

### 3. Developer — per workspace

The developer provisions their own workspace from a selected project flavor — primarily
**self-service through the Developer Portal** (see [Developer Portal](#developer-portal)
below). The `terraform/workspace/` tier is the **equivalent CLI/operator path** to the same
result (workspace container from the chosen flavor + the Boundary target / per-developer
authorization). Full instructions and verification gates:
[`terraform/workspace/README.md`](terraform/workspace/README.md).

```bash
cd terraform/workspace
# The flavor's image is built + pushed by the PROJECT tier (see the project README) — not here.
terraform init
cp terraform.tfvars.example ravi-main.tfvars       # developer_handle/email, project_name, ssh_port, (optional) job_template_name
terraform workspace new ravi-main                  # one Terraform workspace per developer-workspace
terraform apply -var-file=ravi-main.tfvars -out=ravi-main.tfplan   # review the saved plan, then apply
```

This deploys the workspace container (persistent `/home/dev` host volume, first-boot
clone of the flavor's pinned repo), the Boundary target/alias, the per-developer managed
group + role, and a ready-to-paste `~/.ssh/config` snippet (`ssh_config_path` output). The
**image and git repo are pinned to the chosen flavor** by the project tier — the developer
selects only `job_template_name` (default `dev-workspace`), with no image or repo to set. A
flavor with `node_pool = "gpu"` (e.g. `gpu-workspace`) places the workspace on the GPU worker
node automatically.

The **Developer Portal** performs this same provisioning directly over the HashiStack APIs —
the Terraform developer tier and the portal are two paths to the same Vault-KV templates. See
[Developer Portal](#developer-portal).

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

## Developer Portal

Developers don't run Terraform to get a workspace — they use the **Developer Portal**
(`portal/`), a self-service web app that is the **direct-API equivalent of the
`terraform/workspace` tier**. Both build the **same** per-workspace resource graph from the
**same** Vault-KV job templates (dynamic host volume + Nomad job, Boundary target with injected
Vault-signed certs, per-developer managed group / role / alias) — the portal just does it live
over the HashiStack APIs instead of through Terraform.

```
Carbon React SPA  ──►  Go backend (trusted)  ──►  Vault   (read project descriptors + job templates)
  (IBM Verify OIDC)        authz by groups    ──►  Nomad   (host volume + parse/register job)
                                              ──►  Boundary(host/target/managed-group/role/alias)
```

- **Login & authorization.** The developer signs in with **IBM Verify SSO** (the portal's own
  OIDC app — Authorization Code + PKCE) and sees only the projects whose `developers_group_name`
  is in their `groups` claim. This is separate from the *SSH-time* Boundary login run when
  connecting.
- **Discovery & template picker.** The project tier publishes a per-project descriptor to Vault
  KV (`terraform/project/portal.tf` → `secret/projects/<project>/portal-descriptor`); the portal
  reads these to show one **flavor card** per template (its label, description, git repo, image,
  and capability tags). The developer never sets an image or repo — both are pinned by the
  project. A `gpu-workspace` flavor places the workspace on the GPU worker node automatically.
- **Connect.** Each workspace card carries copy-paste **VSCode Remote-SSH** instructions; a small
  local **`secured-ws://` helper** runs the Boundary login and writes the managed `~/.ssh/config`
  block (and removes it on destroy).

> **Status: PoC.** The portal runs locally against the live HashiStack; production hardening and
> an NLB-hosted deployment are on the roadmap. See [`portal/README.md`](portal/README.md).

## Teardown (destroy the environment)

Teardown runs in the **reverse** of the apply order — **developer → project → platform** — so
the Boundary/Nomad/Vault control planes each tier talks to remain up until the platform node is
torn down last. Vault is unsealed for the developer and project destroys, which revoke
Boundary/Nomad/Vault resources through those APIs; a sealed Vault makes them hang or error.
`terraform destroy` prints a plan and prompts before deleting, and on the shared clusters the
plan is reviewed rather than applied with `-auto-approve`.

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

Order matters: the platform tier cannot be torn down while any project or developer state
still exists, because those tiers' providers read the platform's addresses and credentials from
its state — with no control plane left to call, their destroys fail. The lower tiers are always
emptied first.

Once a tier's resources are gone, the now-empty per-instance state can be dropped
(`terraform workspace select default && terraform workspace delete <name>`), and the gitignored
`*.tfvars` and `*.tfplan` files removed.

## Repository layout

```
.
├── terraform/
│   ├── infra/          # PLATFORM tier (once) — clusters, IBM Verify SSO, org scope, WIF, KV,
│   │   │               #   GitHub secrets-plugin registration (vault-github-plugin.tf)
│   │   ├── ami/        # Packer images: base_image (Boundary/Nomad/Vault/Consul +ent + baked
│   │   │               #   GitHub plugin) and gpu_image (NVIDIA driver + toolkit + device plugin)
│   │   ├── config/     # cluster HCL, systemd units, licenses
│   │   └── modules/    # secured-codespace (+ GPU worker, node_pool "gpu") · identity · nomad-vault-wif
│   ├── project/        # PROJECT tier (per project) — FLAT root (no child module);
│   │   │               #   github.tf = per-project GitHub App token broker
│   │   ├── templates/  #   Nomad job templates (raw HCL → Vault KV) — dev-workspace, gpu-workspace
│   │   └── images/     #   Dockerfile per flavor: dev-workspace/ + gpu-workspace/ (CUDA)
│   └── workspace/      # DEVELOPER tier (per workspace) — FLAT root (no child module)
└── portal/             # DEVELOPER PORTAL — self-service web app (direct-API equivalent of terraform/workspace)
    ├── backend/        #   Go: cmd/portal + internal/{config,auth,hashistack,descriptor,portgen,jobrender,workspace,api}; serves the SPA from web/
    ├── frontend/       #   Vite + React + @carbon/react (build output → backend/web)
    └── helper/         #   macOS secured-ws:// helper (Boundary login + ~/.ssh/config management)
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

## Operational constraints

- `*.tfvars`, plan files, and `generated/` artifacts embed the Boundary admin password and
  Vault/Nomad tokens; all are gitignored and are never committed.
- On the shared Boundary/Nomad/Vault clusters, changes are applied from a **saved, reviewed
  plan** (`terraform apply -out=<file>.tfplan`) rather than with `-auto-approve`, and the plan
  file is deleted afterward.
- Vault uses Shamir unseal, so it returns sealed after any node reboot and is re-unsealed before
  Terraform runs or connections are made (see the platform README).
- The workspace host volume lives on the node root EBS: data survives **stop/start + reboot**,
  but not instance replacement.
```