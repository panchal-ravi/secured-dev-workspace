# Secured dev workspace — three-tier Terraform stack

A central, secured remote development workspace on an all-in-one **HashiStack** (Boundary + Nomad + single-node Vault Enterprise) on AWS. Developers reach a per-developer workspace container over **VSCode Remote-SSH brokered by Boundary**, authenticated with **JIT Vault-signed SSH certificates** — no static keys, no passwords, no standing access.

The stack is provisioned as **three Terraform tiers**, each its own root, applied in order. This README is the overview and the end-to-end flow; each tier's own README has the detailed steps.

## Layout

The stack is organized into **three tiers**, each its own Terraform root, applied in order — built this way so the **Developer Portal** (`portal/`, a working PoC) and a future CLI can drive the lower two one instance at a time over the HashiStack APIs:

- **Platform tier** *(once)* — the foundation root `terraform/infra/`: the Boundary/Nomad/Vault clusters (plus an optional **GPU worker node** in the `gpu` Nomad pool and an optional bare-metal **microVM worker node** in the `microvm` pool for Kata-isolated workspaces), IBM Verify OIDC apps + SSO wiring, the admin/readonly managed groups, the Boundary **org** scope, the shared Nomad↔Vault WIF auth method, a Vault KV mount for project artifacts, and the centralized **AI gateways** — the ContextForge **MCP gateway** (governs tools/data over MCP) and the LiteLLM **LLM gateway** (governs model access — routes every workspace's Claude Code to the provider; + its Postgres).
- **Project tier** *(per project)* — the `terraform/project/` root: a Boundary **project** scope, a Nomad namespace, a per-project Vault **namespace** with its SSH CA (`ssh`), the Boundary Vault credential store + SSH credential library, a per-project WIF role, a namespace ACL, and the project's **per-template** Nomad job templates (each pinning its own repo + image + node pool) saved to Vault KV, plus a portal descriptor for the Developer Portal.
- **Developer tier** *(per workspace)* — the `terraform/workspace/` root: spins up a workspace container in the project namespace from a **selected flavor** (its image, repo, and node pool pinned by the project) and creates the Boundary target/alias/role for transparent VSCode Remote-SSH.

```
terraform/
├── infra/                          # PLATFORM root → secured-codespace + identity + nomad-vault-wif + KV mount
│   ├── ami/base_image/             # Packer: builds <owner>-boundary-enterprise-* AMI
│   ├── ami/gpu_image/              # Packer: builds <owner>-gpu-workspace-* AMI (NVIDIA driver + toolkit + nomad-device-nvidia)
│   ├── ami/microvm_image/          # Packer: builds <owner>-microvm-workspace-* AMI (Kata Containers + pinned Docker 27.5.1; bare-metal KVM gates)
│   ├── config/                     # Boundary/Nomad/Vault HCL, systemd units, licenses
│   ├── modules/
│   │   ├── secured-codespace/      # PLATFORM: VPC, NLB, SGs, TLS, EC2 (+ GPU worker "gpu" + microVM worker "microvm"), bootstrap (Boundary+Nomad+Vault, org scope)
│   │   ├── identity/               # PLATFORM: IBM Verify OIDC SSO for Boundary + Nomad (admin/readonly groups)
│   │   └── nomad-vault-wif/        # PLATFORM: the shared jwt-nomad auth method (Nomad↔Vault WIF)
│   ├── vault-github-plugin.tf      # PLATFORM: register the GitHub secrets plugin into Vault's catalog
│   ├── mcp-gateway.tf · llm-gateway.tf  # PLATFORM: ContextForge MCP + LiteLLM LLM gateways (Nomad jobs, ns infra)
│   └── generated/                  # runtime artifacts (SSH key, init JSON) — gitignored
├── project/                        # PROJECT root (flat — one Terraform workspace per project)
│   │                               #   per-project scope, namespace, Vault SSH CA, cred store/library, WIF role, per-template job templates (repo+image+node_pool), portal descriptor
│   ├── github.tf                   #   per-project GitHub App token broker (github + permission set)
│   ├── templates/                  #   Nomad job templates (raw HCL, published to Vault KV) — one "flavor" each (dev-workspace, gpu-workspace, microvm-workspace)
│   └── images/                     #   Dockerfile per flavor: dev-workspace/ + gpu-workspace/ (CUDA) — project-owned, pinned per template (microVM reuses the dev-workspace image)
└── workspace/                      # DEVELOPER root (flat — one Terraform workspace per workspace)
                                    #   reads the chosen flavor's template + its pinned image/repo + node pool from project state
```

The **platform** root (`terraform/infra/`) holds a single Terraform state: the base node (`modules/secured-codespace`, which also runs Vault), the IBM Verify SSO layer (`modules/identity`), the shared Nomad↔Vault WIF auth method (`modules/nomad-vault-wif`) and the KV mount for job templates. Its module calls are in `main.tf`; its provider configs in `providers.tf`. The **project** and **developer** roots are **flat** roots (no child module — each tier was a single-instance wrapper, so the resources live directly in the root) that read the platform root's outputs via `terraform_remote_state`, applied once per project / per workspace (isolated with `terraform workspace`).

## Provisioning order

Apply the tiers in sequence; each lower tier reads the one above via `terraform_remote_state`, so there is no token/address copying between them.

1. **Platform** *(once)* — `terraform/infra/` → see [`infra/README.md`](infra/README.md). Builds the base AMI (and, when enabled, the **GPU** and **microVM** AMIs) and the all-in-one node — plus the optional GPU worker (a Nomad client in the `gpu` pool) and the optional bare-metal microVM worker (a Nomad client in the `microvm` pool) — bootstraps Boundary + Nomad + Vault, wires IBM Verify SSO, and stands up the shared `jwt-nomad` WIF anchor + the KV mount for job templates.
2. **Project** *(per project)* — `terraform/project/` → see [`project/README.md`](project/README.md). Creates, scoped to the project: a Boundary project scope, a Nomad namespace, a Vault namespace + SSH CA (`ssh`) + signing role, a least-privilege Boundary credential store + SSH credential library, a per-project WIF role + namespace ACL, a GitHub App token broker, and the project's job templates in Vault KV.
3. **Developer** *(per workspace)* — `terraform/workspace/` → see [`workspace/README.md`](workspace/README.md). Builds + pushes the workspace image, then deploys a persistent workspace container in the project namespace and creates the Boundary target/alias + per-developer OIDC role for transparent VSCode Remote-SSH.

> **Vault must be unsealed** for the project and developer tiers (they create Vault mounts/roles and Boundary signs each session cert through Vault). With Shamir unseal the node comes back **sealed** after a reboot — re-unseal first (see `infra/README.md`).

## Verify the Boundary-brokered workspace SSH

The end-to-end developer demo (run from `terraform/workspace/` after applying). Two connect paths are **live-verified (2026-06-02)**: VSCode Remote-SSH (and plain `ssh <alias>`) via the **Boundary Client Agent + transparent sessions**, and the CLI `boundary connect ssh` as the no-Client-Agent fallback. The CLI path is shown below; the transparent-session / VSCode steps are in `terraform/workspace/README.md` ("VSCode Remote-SSH via transparent sessions").

1. **SSO-authenticate to Boundary as the developer** (same IBM Verify login):
   ```bash
   eval "$(terraform -chdir=../infra output -raw boundary_oidc_login_command)"   # browser OIDC flow as e.g. alice
   ```
2. **Connect with `boundary connect ssh`** (routes via the co-located worker proxy on 9202 — nothing is on the NLB). Boundary signs a fresh Vault cert and injects it — **no local key**:
   ```bash
   boundary connect ssh -tls-insecure \
     -target-id "$(terraform output -json workspace_target_ids | jq -r '."alice/main"')" \
     -- whoami        # → dev
   ```
The cert `key_id` is alice's email (in the workspace `sshd` stderr). For **VSCode Remote-SSH** over these injected targets, use the **Boundary Client Agent + transparent sessions** path documented in `terraform/workspace/README.md` (plain `ssh <alias>` / a normal VSCode `Host` entry — no `ProxyCommand`).
3. **Negative isolation test (required):** SSO-authenticate as **bob** — IBM Verify forces a fresh login every time (the OIDC method sets `prompts = ["login"]`), so enter bob's credentials rather than reusing alice's session — then `boundary connect ssh` to *alice's* target id is **DENIED** (bob's managed group has no grant on alice's target). **Verified 2026-06-02.**
4. **Reachability negative:** on the node `ss -tlnp | grep 222` shows the SSH host port bound, but off-box `nc -vz <nlb-dns> 2222` **fails** — Boundary is the only path in.
