# Terraform — the platform tier

A central, secured remote development workspace on an all-in-one **HashiStack** (Boundary + Nomad +
single-node Vault Enterprise) on AWS. Developers reach a per-developer workspace container over
**Remote-SSH brokered by Boundary**, authenticated with **JIT Vault-signed SSH certificates** — no
static keys, no passwords, no standing access.

**Terraform provisions the platform, and stops there.** One root — [`infra/`](infra/) — stands up the
clusters, the identity layer, the gateways, the storage drivers and the Developer Portal. Everything
above that line is **Portal-driven**: projects, MCP servers, workspace templates, shared volumes,
workspaces and AI agents are all created through the Portal UI/API against the running HashiStack,
with no further Terraform run.

- **Stand the platform up** → [`infra/README.md`](infra/README.md)
- **Then drive all three roles end to end** → [`infra/E2E-WALKTHROUGH.md`](infra/E2E-WALKTHROUGH.md)
- **The Portal itself** (Go + Carbon React) → [`../portal/README.md`](../portal/README.md)

## Layout

```
terraform/
├── infra/                          # THE Terraform root — one state for the whole platform
│   ├── main.tf                     #   calls the three modules below
│   ├── ami/base_image/             # Packer: <owner>-boundary-enterprise-* (all-in-one node)
│   ├── ami/agent_image/            # Packer: agent-pool worker (MCP servers, AI agents)
│   ├── ami/gpu_image/              # Packer: NVIDIA driver + toolkit + nomad-device-nvidia
│   ├── ami/microvm_image/          # Packer: Kata Containers + pinned Docker (bare-metal KVM)
│   ├── config/                     # Boundary/Nomad/Vault HCL, systemd units, licenses
│   ├── modules/
│   │   ├── secured-codespace/      #   VPC, NLB, SGs, TLS, EC2 (+ agent/GPU/microVM workers),
│   │   │                           #   EFS, IAM, bootstrap (Boundary + Nomad + Vault, org scope)
│   │   ├── identity/               #   IBM Verify OIDC SSO for Boundary + Nomad + the Portal
│   │   └── nomad-vault-wif/        #   the shared jwt-nomad auth method (Nomad↔Vault WIF)
│   ├── developer-portal.tf         # Portal Nomad job + its OIDC app + workload identities
│   ├── portal-postgres.tf          #   its control-plane database
│   ├── platform-admin.tf           # the onboarding plane (MCP deploys, LLM models)
│   ├── mcp-gateway.tf              # ContextForge MCP gateway  (governs tools/data)
│   ├── llm-gateway.tf              # LiteLLM gateway + Postgres (governs model access)
│   ├── ebs-csi.tf · efs-csi.tf     # durable per-workspace /home/dev · shared /shared volumes
│   ├── nomad-boundary-host-sync.tf # keeps a rescheduled workspace's Boundary target following it
│   ├── agent-identity.tf           # Vault identity-OIDC issuer for agent actor JWTs
│   ├── vault-github-plugin.tf      # registers the GitHub secrets plugin in Vault's catalog
│   ├── vault-kv.tf · demo-db.tf    # KV mount for project artifacts · optional demo database
│   └── generated/                  # runtime artifacts (SSH key, init JSON) — gitignored
├── project/                        # RETIRED — documentation only, no Terraform config
└── workspace/                      # SUPERSEDED by the Portal — config kept for legacy state
```

Everything in `infra/` shares **one** Terraform state. It is applied in three ordered stages (the NLB
must exist before the boundary/nomad/vault providers can connect, and the WIF anchor must exist
before the jobs that use it) — see [`infra/README.md`](infra/README.md) §5.

## What replaced the old project and workspace tiers

This used to be a three-tier stack: `infra/` → `project/` (per project) → `workspace/` (per
workspace), each its own root, chained with `terraform_remote_state`. Both lower tiers are gone from
the provisioning path.

| Was | Is now |
|---|---|
| `terraform apply` in `project/`, one Terraform workspace per project | **Portal → Projects (admin) → New project.** One action provisions the Vault child namespace + WIF roles, the Nomad namespace + ACLs, the Boundary project scope + host catalog, the SSH CA, the `github` mount, the LLM virtual key and the Boundary credential store |
| `terraform apply` in `workspace/`, one Terraform workspace per developer | **Portal → New workspace.** The developer picks a flavor and shared volumes; name, port and disk are server-generated |
| Job templates hand-written into Vault KV | Base templates + per-project flavors in the Portal's own Postgres, rendered at launch |
| MCP servers wired by a provisioning script | **Portal → mcp servers → Deploy MCP server**, with a built-in scoped-access test |

`project/` now holds only its README and runbook — there is no `.tf` left to apply. `workspace/`
still has its configuration, but only so pre-Portal state can be destroyed; do not use it to create
anything.

## How a developer reaches a workspace

Unchanged by the move to the Portal, and the reason the platform exists:

1. The developer signs in to the Portal with IBM Verify, opens their workspace, and the local helper
   (or `boundary connect ssh`) brokers the session. Boundary reuses the Verify SSO session, so no
   second login.
2. Boundary signs a short-lived SSH certificate through the **project's** Vault SSH CA and injects
   it. The developer holds **no SSH key**; the workspace `sshd` trusts only that CA
   (`TrustedUserCAKeys`, `AuthorizedKeysFile none`). The cert's `key_id` is the developer's email, so
   every session is attributable.
3. The workspace's SSH port is bound on the node but is **not** reachable off-box — Boundary is the
   only path in. `nc -vz <nlb-dns> <ssh-port>` from outside fails by design.

Authorization is per-target: a developer's Boundary role grants `authorize-session` on their own
workspace only, so another project member cannot connect to it. To exercise that denial you must
sign in as the other user — the Boundary OIDC method deliberately sets **no `prompts` override** so
that Portal→Boundary SSO stays silent, which also means Verify will reuse the session already in the
browser. Sign out of the Portal (RP-initiated logout ends the Verify session) or use a separate
browser profile.

The full walkthrough, including what to check from inside the workspace, is
[`infra/E2E-WALKTHROUGH.md`](infra/E2E-WALKTHROUGH.md) §3.

> **Vault must be unsealed** for every plan and apply, and for Boundary to sign session certs. With
> Shamir unseal the node comes back **sealed** after a reboot — re-unseal first (see
> [`infra/README.md`](infra/README.md) §7).
