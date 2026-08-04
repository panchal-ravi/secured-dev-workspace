# Platform tier — stand up the secured dev workspace (`terraform/infra/`)

Minimum steps to build and verify the platform tier. For architecture, module internals and the deep
verification recipes, see [`README.md.bak`](./README.md.bak) (the previous, full-detail README).
Once the platform is up, **[`E2E-WALKTHROUGH.md`](./E2E-WALKTHROUGH.md)** takes over: it drives the
platform-admin, project-admin and developer roles through the Portal UI (§10). Also related:
[`PLATFORM-ADMIN-RUNBOOK.md`](./PLATFORM-ADMIN-RUNBOOK.md) (admin-plane operations) and
[`../README.md`](../README.md) (three-tier overview).

## What this provisions

One all-in-one EC2 running the **Boundary** controller+worker (local Postgres, AEAD KMS), a combined
**Nomad** server+client (TLS + ACLs), and a single-node **Vault** — all behind a public NLB whose every
listener is locked to the `/32` of the machine that ran `terraform apply`.

On top of it, Nomad jobs in namespace `infra`: the **ContextForge MCP gateway**, the **LiteLLM LLM
gateway** (+Postgres), the **AWS EBS CSI driver** (durable per-workspace `/home/dev`), the **Developer
Portal** (+Postgres), the **Nomad→Boundary host-sync** reconciler, and — when enabled — the **EFS CSI
driver** for shared volumes and a throwaway demo Postgres.

Optional extra EC2s join as Nomad clients in their own node pools: `agents` (MCP servers + AI agents),
`gpu` (NVIDIA T4), `microvm` (Kata, bare metal).

---

## 1. Prerequisites

**Tooling**

- AWS credentials in the environment.
- Packer >= 1.9, Terraform >= 1.7.
- Docker with `buildx`, and `docker login` to your registry (the image build scripts always `--push`).
- **macOS** — required to build the portal image (the connect helper uses `osacompile` / `PlistBuddy`).

**Enterprise licenses** — Boundary, Nomad **and** Vault. Place them here (all gitignored; `main.tf`
reads all three with `file()` and the apply hard-fails if any is missing):

```
config/boundary_license.hclic
config/nomad_license.hclic
config/vault_license.hclic
```

**IBM Verify SaaS** (manual, one-time):

1. A Verify tenant.
2. A bootstrap **API client** (console → Security → API access) with entitlements
   `manageAppAccessAdmin` **and** `readAppConfigAndClientSecret`.
3. Groups for admin / read-only access, matching `admin_group_name` / `readonly_group_name`.
4. A group named **exactly `platform-admins`** holding your platform admins. The match is
   case-insensitive but the name is literal — singular `platform-admin` does **not** work. Without it
   `/api/me` returns no roles and every `/api/admin/*` call 403s.
5. A group per project (`<project>-developers`, e.g. `project-acme-developers`) once you onboard
   projects.
6. **Free an app slot.** The tenant enforces a **5-application cap** and a full stack uses 4. Delete any
   hand-registered `secured-codespace-portal` app before applying, or the portal app create fails with
   `CSIAD0030 (exceeded the allowed limit of 5 applications)`. Terraform creates that app for you.

> **Your egress IP matters.** Every NLB listener and SSH is locked to the public `/32` detected at apply
> time. If you move networks, re-apply from the new IP (or add it to the `<owner>-boundary-nlb` security
> group for ports 8200/4646/9200/9202/4444/4000/8443).

---

## 2. Build the AMIs

Always build the base image. Build the others only if you enable the matching node.

```bash
cd ami/base_image                                          # always
packer init . && packer build -var-file=variables.pkrvars.hcl .    # -> <owner>-boundary-enterprise-<ts>

cd ../agent_image                                          # enable_agent_nodes = true
packer init . && packer build -var-file=variables.pkrvars.hcl .    # -> <owner>-agent-node-<ts>

cd ../gpu_image                                            # enable_gpu_node = true
packer init . && packer build -var-file=variables.pkrvars.hcl .    # -> <owner>-gpu-workspace-<ts>

cd ../microvm_image                                        # enable_microvm_node = true — build ON c5.metal
packer init . && packer build -var-file=variables.pkrvars.hcl .    # -> <owner>-microvm-workspace-<ts>
```

Terraform finds each AMI by an `<owner>-*` name filter, so `owner` in `terraform.tfvars` must match the
`owner` used for the Packer build.

---

## 3. Build the container images

Only **two** images are built by hand. Everything else (`mcp_gateway_image`, `litellm_image`,
`litellm_postgres_image`, `portal_postgres_image`, `ebs_csi_driver_image`, `efs_csi_driver_image`) is
pulled from a public registry.

### 3a. macOS connect helper — required before the portal build

`portal/Dockerfile` does `COPY --chown=10001:10001 backend/helper-dist /app/helper-dist`, and
`portal/.gitignore` ignores `backend/helper-dist/`. **On a fresh clone that directory does not exist and
the portal image build fails.** Build it first:

```bash
cd <repo-root>
portal/helper/macos/build.sh        # -> portal/backend/helper-dist/SecuredWS-macos.zip
```

### 3b. Developer Portal image

`portal/scripts/build-image.sh` builds the Carbon/React frontend **and** the Go backend inside Docker
(you pre-build neither) and always pushes, so the registry login and a push-capable builder must be in
place first.

**1. Work from the repo root** — every path below is relative to it (step 3a already put you there):

```bash
cd <repo-root>
git status        # build from the commit you intend to ship; the image carries no version stamp
```

**2. Authenticate to the registry** (the script always `--push`es; a missing login fails the build):

```bash
docker login
```

**3. Create a push-capable buildx builder — once per machine.** The default `docker` driver cannot
`--push`; the `docker-container` driver can:

```bash
docker buildx create --name multiarch --use --bootstrap   # once; skip if it already exists
docker buildx ls                                          # 'multiarch' should be the active default
```

**4. Confirm the helper artifact from step 3a exists** — the Dockerfile copies it, and it is gitignored:

```bash
ls -l portal/backend/helper-dist/SecuredWS-macos.zip
```

**5. Build and push.** The script accepts exactly two environment variables:

```bash
PORTAL_IMAGE=<registry-user>/developer-portal:<tag> \
PORTAL_PLATFORM=linux/amd64 \
  portal/scripts/build-image.sh
```

| Variable | Default | Notes |
| --- | --- | --- |
| `PORTAL_IMAGE` | `panchalravi/developer-portal:poc` | Full `repo:tag`. Prefer a fresh, distinct tag over reusing a mutable one. |
| `PORTAL_PLATFORM` | `linux/amd64` | Leave it — every Nomad node pool is amd64. |

The script prints `Pushed <image>` on success.

**6. Verify the push landed** (reads the manifest back from the registry):

```bash
docker buildx imagetools inspect <registry-user>/developer-portal:<tag>
```

**7. Pin the tag** in `terraform.tfvars`:

```hcl
developer_portal_image = "<registry-user>/developer-portal:<tag>"
```

On a first build that is all — the stage-3 `terraform apply` in §5 deploys the job.

**Rebuilding later, on an already-running stack**, is the only case that needs a targeted apply:

```bash
# New, distinct tag: pin it in terraform.tfvars, then push just this job
terraform apply -target=nomad_job.developer_portal

# Reusing a mutable tag (e.g. :poc): Terraform sees no change, so force a restart
nomad job restart -namespace infra -reschedule -on-error=fail developer-portal
```

### 3c. Nomad→Boundary host-sync image

No build script exists — only a Dockerfile. The nodes are amd64, so on Apple Silicon this must be a
`buildx` cross-build:

```bash
docker buildx build --platform linux/amd64 \
  -t <registry-user>/nomad-boundary-host-sync:<tag> --push nomad-boundary-host-sync/
```

Pin the result as `nomad_boundary_host_sync_image` in `terraform.tfvars`:

```hcl
nomad_boundary_host_sync_image = "<registry-user>/nomad-boundary-host-sync:<tag>"
```

Nothing else to run here — the stage-3 `terraform apply` in §5 deploys the job. (The job is gated
on `enable_developer_portal`.)

### 3d. Agent runtime image (only if you use AI agents)

```bash
AGENT_RUNTIME_IMAGE=<registry-user>/agent-runtime:<tag> portal/images/agent-runtime/build-image.sh
```

This is **not** a Terraform variable — the portal defaults to `panchalravi/agent-runtime:agentv2`
(`PORTAL_AGENT_RUNTIME_IMAGE`), so either that tag must exist or you override the env var.

> **Workspace images** (`workspace-base`, `gpu-workspace`) belong to the project tier, not here. The
> tags the Portal seeds are in `portal/backend/internal/jobtemplate/seeds.go`; a reference Dockerfile
> lives at `docs/handoff/workspace-base.Dockerfile` (note: `docs/` is gitignored, so that one is a
> local working copy, not part of the repo).

---

## 4. Configure `terraform.tfvars`

```bash
cd terraform/infra
cp terraform.tfvars.example terraform.tfvars     # gitignored — never commit secrets
```

Everything you need lives in that one file. (Terraform also auto-loads any `*.auto.tfvars`, so an
existing `portal.auto.tfvars` keeps working — but you do not need one.)

Four variables have **no default** and the plan will not run without them:

```hcl
# --- Required: no defaults ---
ibm_verify_tenant            = "myorg.verify.ibm.com"      # hostname only, no scheme
ibm_verify_api_client_id     = "00000000-0000-0000-0000-000000000000"
ibm_verify_api_client_secret = "..."                       # sensitive
deepseek_api_key             = "sk-..."                    # sensitive; the ONE central provider key
```

The base + feature settings you will normally touch:

```hcl
# --- Base ---
owner         = "rp"                # must match the owner used for the Packer builds
region        = "ap-southeast-1"
instance_type = "t3.xlarge"

# --- Feature flags ---
enable_agent_nodes      = true      # 'agents' node pool — needed by the platform-admin MCP plane
enable_developer_portal = true      # the Portal Nomad job + its Verify OIDC app
enable_platform_admin   = true      # in-portal onboarding plane + portal-postgres
enable_shared_volume    = true      # EFS + EFS CSI driver for per-project /shared volumes
enable_demo_db          = false     # throwaway Postgres for the postgres-mcp blueprint demo
enable_gpu_node         = false     # costly — needs the GPU AMI
enable_microvm_node     = false     # costly (c5.metal) — needs the microVM AMI

# --- Images you built in step 3 ---
developer_portal_image         = "<registry-user>/developer-portal:<tag>"
nomad_boundary_host_sync_image = "<registry-user>/nomad-boundary-host-sync:<tag>"

# --- Portal OIDC: the FULL issuer endpoint, not the bare tenant host ---
portal_oidc_issuer = "https://myorg.verify.ibm.com/oidc/endpoint/default"

# --- Boundary bootstrap admin (break-glass; SSO is layered on top) ---
boundary_admin_login_name = "admin"
boundary_admin_password   = "..."   # sensitive
boundary_org_name         = "primary-org"
```

See `terraform.tfvars.example` for the complete annotated set, including the Verify group names, the
gateway image pins, and the GitHub-plugin version/SHA pair.

---

## 5. Provision

A fresh stack applies in **three ordered stages**. The `boundary`/`nomad`/`vault`/`restapi` providers
connect to the base node over the NLB, so the base must exist before they can configure anything; and
`agent-identity.tf` reads the `jwt-nomad` mount at **plan** time, so that mount must exist before the
plain apply.

Run every Terraform command with a clean environment — stale `AWS_*` / `VAULT_*` / `NOMAD_*` variables
from a previous build silently shadow the provider configuration and are a repeat cause of failures:

```bash
alias tf='env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
              -u VAULT_ADDR -u VAULT_TOKEN -u NOMAD_ADDR -u NOMAD_TOKEN terraform'
```

```bash
tf init

# Stage 1 — the base node: VPC, NLB, EC2, Boundary + Nomad + Vault bootstrap.
# Blocks on cloud-init and writes generated/{boundary,nomad,vault}-setup.json.
tf apply -target=module.secured_codespace

# Stage 2 — the shared jwt-nomad Vault auth method (Nomad↔Vault WIF trust anchor).
# MUST precede the plain apply, or it fails with "No secret engine mount at auth/jwt-nomad/".
tf apply -target=module.nomad_vault_wif

# Stage 3 — everything else: Verify OIDC apps + SSO wiring, KV mount, GitHub plugin,
# MCP + LLM gateways, EBS/EFS CSI, developer portal, host-sync.
tf apply
```

**Stage 3 must be non-targeted.** The `boundary` provider takes its address from the base module's
outputs; a targeted-only run can leave that evaluation empty and the apply fails with
`boundary: error parsing address`.

Once the base and the `jwt-nomad` mount exist, later changes need only a plain `tf apply`.

> **If an apply fails**, work through [§7 Troubleshooting](#7-troubleshooting) — it comes after Verify
> because every diagnostic there needs the operator environment exported in §6.


---

## 6. Verify

Set up the operator environment once:

```bash
cd terraform/infra
export VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true
export VAULT_TOKEN="$(terraform output -raw vault_root_token)"
export NOMAD_ADDR="$(terraform output -raw nomad_addr)" NOMAD_SKIP_VERIFY=true
export NOMAD_TOKEN="$(terraform output -raw nomad_management_token)"
```

**Vault**

```bash
vault status                              # Initialized: true, Sealed: false
vault plugin list secret | grep github    # the baked GitHub secrets plugin is registered
vault kv get secret/infra/llm-gateway     # master_key, salt_key, deepseek_api_key, portal_admin_key
vault kv get secret/infra/mcp-gateway     # jwt_secret_key, admin_email, admin_password
```

**Nomad**

```bash
nomad server members                      # the server is "alive"
nomad node status                         # all-in-one (default pool) + agents-pool client(s) "ready"
nomad job status -namespace infra         # developer-portal, mcp-gateway, litellm-gateway,
                                          # litellm-postgres, portal-postgres,
                                          # ebs-csi-controller, ebs-csi-node,
                                          # nomad-boundary-host-sync
                                          # (+ efs-csi, demo-db when enabled)
nomad plugin status aws-ebs               # Controllers + Nodes healthy
nomad plugin status aws-efs               # only when enable_shared_volume = true
nomad namespace list                      # default, infra, infra-mcp
```

The Nomad UI is at `$(terraform output -raw nomad_ui_addr)` — sign in under **ACL** with the management
token.

**Shared-volume EFS filesystem** (only when `enable_shared_volume = true`). The module creates it but
the root does not re-export its id, so query AWS directly — it is named `<owner>-boundary-shared`:

```bash
aws efs describe-file-systems --region <your-region> \
  --query 'FileSystems[].{Id:FileSystemId,Name:Name,State:LifeCycleState}' --output table
```

**Gateways and Portal**

```bash
curl -s  "$(terraform output -raw llm_gateway_addr)/health/liveliness"   # "I'm alive!"
curl -sk "$(terraform output -raw developer_portal_addr)/health"         # {"status":"ok"}
```

The MCP gateway is JWT-only (no anonymous health check) — the token-minting recipe is under
"Verify the MCP Gateway" in [`README.md.bak`](./README.md.bak). Its Admin UI is at
`$(terraform output -raw mcp_gateway_addr)`, credentials in `secret/infra/mcp-gateway`.

**Boundary** — break-glass password login (self-signed cert):

```bash
boundary authenticate password \
  -addr "$(terraform output -raw boundary_addr)" \
  -auth-method-id "$(terraform output -raw admin_auth_method_id)" \
  -login-name "$(terraform output -raw admin_login_name)" \
  -tls-insecure
```

**SSO** — Verify-backed logins for Boundary and Nomad:

```bash
eval "$(terraform output -raw boundary_oidc_login_command)"   # browser OIDC flow
eval "$(terraform output -raw nomad_oidc_login_command)"      # CLI loopback flow
```

**Portal login** — open `$(terraform output -raw developer_portal_addr)` and sign in with Verify. A
member of `platform-admins` should see `"roles":["platform-admin"]` at `/api/me`; anyone else must get
403 on `/api/admin/*`.

> Use a **normal browser window, not incognito** — the Portal→Boundary single sign-on relies on the
> shared Verify session.

**Hand this to your project-admins** — the node's private IP. Project-admins deploying an MCP server
that talks to Vault must type it into the server's `VAULT_ADDR`, and nothing in the Portal exposes it.
A rebuild issues a new one:

```bash
terraform output -raw instance_private_ip
```

---

## 7. Troubleshooting

All commands here assume the operator environment from §6 is exported.

### The LiteLLM portal-admin key bootstrap failed

**What it is.** The Portal's admin plane manages LLM models on the gateway (`POST /model/new`, etc.). It
must never use the gateway **master key**, so `terraform_data.litellm_portal_admin_key`
(`platform-admin.tf:92`, only when `enable_platform_admin = true`) runs
`scripts/litellm-portal-admin-key.sh` after the gateway job. That script creates a **service user inside
LiteLLM's own database** — `user_id = portal-admin`, `user_role = proxy_admin` — mints a key for it, and
merge-patches that key into Vault at `secret/infra/llm-gateway` under `portal_admin_key`. The Portal reads
that field over WIF at startup and sends it as `Authorization: Bearer` on every gateway admin call.

`portal-admin` is **not** a human and not an IBM Verify identity — it exists only in LiteLLM, and it is
the Portal's service account there. Do not confuse it with the Verify group `platform-admins`, which is
what grants *people* the Portal's platform-admin role.

**Why it fails.** The script reaches the gateway over the **NLB** and waits at most **60 s** for
`/health/liveliness`. Terraform has already waited for the Nomad alloc to be healthy (`detach = false`),
but the NLB target group runs its own TCP health check on `:4000` and only routes once that passes — so
on a cold build the alloc can be up while the NLB is not yet forwarding. If the 60 s budget runs out the
script exits non-zero, and the provisioner has no `on_failure = continue`, so the apply fails.

**How to tell.** Four signals, cheapest first:

```bash
# 1. The apply's own error — the script's message on stderr:
#      ERROR: could not mint portal-admin key
#    Terraform then reports the tainted resource:
#      Error: local-exec provisioner error

# 2. Authoritative check — is the key actually in Vault?
#    Prints the sk-... value on success; errors with
#    "field 'portal_admin_key' not present in secret" if the bootstrap never completed.
vault kv get -field=portal_admin_key secret/infra/llm-gateway

# 3. Is the Portal restart-looping because of it? Look for a non-zero restart count,
#    then grep its stderr for the startup error.
nomad job status -namespace infra developer-portal
nomad alloc logs -stderr -namespace infra \
  "$(nomad job allocs -namespace infra -json developer-portal | jq -r '.[0].ID')" \
  | grep -i "admin plane"
#    -> "admin plane: read llm-gateway portal-admin key: ..."

# 4. Root cause — can you reach the gateway through the NLB at all?
#    If this hangs or refuses, the NLB target is still unhealthy (or your /32 changed).
curl -fsS "$(terraform output -raw llm_gateway_addr)/health/liveliness"
```

A useful confirmation: `terraform plan` will show `terraform_data.litellm_portal_admin_key[0]` as
tainted and forced for replacement — that is what makes the re-run below re-execute the script.

**Fix — re-run the apply.** The NLB target is healthy by then, the script mints the key, and the Portal
starts:

```bash
tf apply
```

**Manual fallback**, if the apply keeps failing (e.g. your `/32` cannot reach the NLB on `:4000`): mint
the key yourself and write it to the same Vault field the script would have.

```bash
# 1. Open the LiteLLM Admin UI and log in with the master key:
terraform output -raw llm_gateway_addr        # then browse <addr>/ui
vault kv get -field=master_key secret/infra/llm-gateway

# 2. In the UI: Virtual Keys -> Create New Key, with User Role = proxy_admin. Copy the sk-... value.

# 3. Write ONLY that subkey — `patch` preserves master_key/salt_key/deepseek_api_key:
vault kv patch secret/infra/llm-gateway portal_admin_key=<sk-...>

# 4. Restart the Portal so it re-reads Vault at startup:
nomad job restart -namespace infra -on-error=fail developer-portal
```

Use `vault kv patch`, never `vault kv put` — `put` replaces the whole secret and would drop the
gateway's master key.

### The Portal's onboarding plane is disabled

The Portal's platform-admin plane turns on only when **both** `PORTAL_MCP_GATEWAY_ADDR` and
`PORTAL_LLM_GATEWAY_ADDR` are set on the task (`config.go:153`). If either is empty the plane boots
disabled and the failures are **silent**: project creation still returns OK but skips engine
auto-provision (the project gets no `ssh/` or `github/` mount, so the project-admin's GitHub save
no-ops), LLM model onboarding 404s, and the project-admin's **New template** button is disabled.

The jobspec pins both addresses statically to the node's stable gateway ports, so this should not
happen on a current build — but check before creating any project:

```bash
PORTAL_ALLOC=$(nomad job allocs -namespace infra -json developer-portal \
  | jq -r '[.[]|select(.ClientStatus=="running")][0].ID')
nomad alloc logs -namespace infra "$PORTAL_ALLOC" | grep -i "onboarding plane"
#  want → "platform-admin onboarding plane enabled  mcp_gateway=…:4444  llm_gateway=…:4000"
nomad alloc exec -namespace infra "$PORTAL_ALLOC" \
  sh -c 'echo "MCP=[$PORTAL_MCP_GATEWAY_ADDR] LLM=[$PORTAL_LLM_GATEWAY_ADDR]"'   # both non-empty
```

**Any project created while the plane was disabled must be deleted and recreated** — engines do not
back-fill.

### Vault is sealed

Vault re-seals on every reboot (Shamir), and **every** plan/apply needs it unsealed — the root `vault`
provider is configured at the root. Symptom: provider errors mentioning `Vault is sealed` / `503`.

```bash
VAULT_ADDR="$(terraform output -raw vault_addr)" VAULT_SKIP_VERIFY=true \
  vault operator unseal "$(terraform output -json vault_unseal_keys | jq -r '.[0]')"
```

### A config change did not reach the running node

`aws_instance.this` has `lifecycle { ignore_changes = all }`, so bootstrap/config edits are never pushed
by a plain apply. Replace the instance, then re-run stages 1–2 (the replace rebuilds Vault):

```bash
tf apply -replace='module.secured_codespace.aws_instance.this'
```

---

## 8. Optional worker nodes

Each is a separate EC2 joining the cluster as a Nomad client in its own node pool, so only flavors that
opt into that pool land there. Rationale and networking detail: [`README.md.bak`](./README.md.bak).

**Agent pool** (`enable_agent_nodes = true`) — standard CPU, node pool `agents`. Where the Portal places
MCP servers and AI agents (`platform_admin_mcp_node_pool`, default `"agents"`); set that to `""` to run
them on the all-in-one node instead. Requires the **agent AMI**. Tune with `agent_node_count` /
`agent_instance_type`.

**GPU** (`enable_gpu_node = true`) — `g4dn.xlarge` NVIDIA T4, node pool `gpu`. Requires the **GPU AMI**.
Costly. Its `remote-exec` **blocks the apply** until the node bootstraps and `nvidia-smi` works. Private
IP surfaces as the `gpu_instance_private_ip` output.

**microVM** (`enable_microvm_node = true`) — `c5.metal` with Kata Containers, node pool `microvm`, for
hardware-isolated workspaces. **Bare metal is mandatory** (Kata needs `/dev/kvm`). Requires the
**microVM AMI**, itself buildable only on `c5.metal`. Very costly. Its `remote-exec` blocks the apply
until `kata-runtime check` and a `docker run --runtime=kata` smoke test pass.

> `enable_default_spare = true` is a **test-only** flag that adds a second `default`-pool client purely
> to exercise a cross-node workspace reschedule. Set it back to `false` and apply when done.

---

## 9. Shared volumes (EFS)

`enable_shared_volume = true` provisions an EFS filesystem, per-subnet mount targets, a dedicated
security group and the node IAM policy that lets the CSI driver create access points — plus the
`aws-efs` CSI driver job. The Portal then cuts **one EFS access point per named shared volume** and
mounts it into workspaces under `/shared`. Volumes themselves are created by a project-admin in the
Portal, not here.

The filesystem ID is not a Terraform output; query it from AWS:

```bash
aws efs describe-file-systems --region <your-region> \
  --query "FileSystems[?Name=='<owner>-boundary-shared'].FileSystemId" --output text
```

> **Cap:** EFS allows 120 access points per filesystem, i.e. **120 shared volumes platform-wide**.

---

## 10. Next steps

The platform tier is up. Everything above this line is Terraform; **everything from here on is done
in the Portal UI** — project onboarding, MCP servers, workspace templates, shared volumes and
workspace creation are all portal-driven, with no further Terraform run.

**→ [`E2E-WALKTHROUGH.md`](./E2E-WALKTHROUGH.md)** walks all three roles end to end against the
platform you just built:

| Section | Role | What it covers |
|---|---|---|
| §1 | **platform-admin** | Onboard an LLM model, review base templates, set the coding-agent allow-list, create a project |
| §2 | **project-admin** | GitHub App credentials, deploy + test MCP servers, grant project-user, publish a workspace template with MCP add-ons, create shared volumes |
| §3 | **project-user** | Create a workspace, connect over Boundary, and verify identity/git/LLM/MCP/shared volumes from inside it |
| §4 | any member | The AI-agents plane (author, deploy, chat) |
| §5 | platform-admin | Delete a project and confirm full teardown |

For the wider three-tier picture, see [`../README.md`](../README.md).

## 11. Destroy

Tear down in reverse-create order, each with the same clean environment:

```bash
for tier in workspace project infra; do
  ( cd terraform/$tier && env -u AWS_ACCESS_KEY_ID -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
      -u VAULT_ADDR -u VAULT_TOKEN -u NOMAD_ADDR -u NOMAD_TOKEN terraform destroy -auto-approve )
done
```

Vault must be **unsealed** for the project and workspace destroys (they remove Vault mounts and roles).
`generated/boundary-setup.json` and `generated/nomad-setup.json` are deleted by the infra destroy.
Known snags — AWS Backup recovery points blocking the backup vault, and the Vault/Nomad/Boundary
providers falling back to `127.0.0.1` once the node is gone — are covered in
[`PLATFORM-ADMIN-RUNBOOK.md`](./PLATFORM-ADMIN-RUNBOOK.md).

To roll back only the admin plane without destroying anything:

```bash
tf apply -var enable_platform_admin=false
```
