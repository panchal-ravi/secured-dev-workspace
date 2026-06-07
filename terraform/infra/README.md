# Platform tier — all-in-one HashiStack node (`terraform/infra/`)

The **platform / foundation tier** of the three-tier model — see
[`../README.md`](../README.md) for the overview, the provisioning order, and the
end-to-end developer demo.

Provisions **HashiCorp Boundary Enterprise** as a single all-in-one node on AWS:

1. A **Packer** base image (`ami/base_image/`) that bakes the Boundary, Consul,
   Nomad and Vault Enterprise binaries onto the org base AMI.
2. A **Terraform** module (`modules/secured-codespace/`) that launches one EC2
   instance from that AMI running, on the same host: the Boundary **controller +
   worker** (backed by a **local PostgreSQL**, static **AEAD KMS keys**) and a
   single combined **Nomad server + client** agent (TLS + ACLs enabled).
3. A second **Packer** GPU image (`ami/gpu_image/`) and a second EC2 — a **GPU worker**
   (`g4dn.xlarge`, NVIDIA T4) — that joins the cluster as a **Nomad client in the `gpu`
   node pool** for GPU workspaces. See [GPU worker node](#gpu-worker-node).

The Boundary controller API (`9200`), Boundary worker proxy (`9202`), the Nomad
HTTP API/UI (`4646`) and the Vault API/UI (`8200`) are all reached through a public **Network Load Balancer**.
SSH is direct to the instance. **All ingress — every NLB listener and SSH — is
locked to the public IP of the machine running Terraform (`/32`)**, auto-detected
via `https://checkip.amazonaws.com`.

## Modules

What this platform tier provisions and the order it applies in (the project and developer
tiers are documented in [`../project/README.md`](../project/README.md) and
[`../workspace/README.md`](../workspace/README.md)):

- **`modules/secured-codespace`** *(base — applied first)* — the all-in-one node and everything it
  depends on: VPC, subnets, security groups (locked to your `/32`), the public NLB, the self-signed TLS
  certs, and the EC2 instance whose cloud-init bootstraps the **Boundary controller+worker** (local
  PostgreSQL + AEAD KMS), the combined **Nomad server+client** (ACLs + TLS), and the single-node
  **Vault** server (init + unseal). Emits all the connection outputs (`*_addr`, admin creds, scope IDs,
  tokens) the day-2 modules and providers consume.
- **`modules/identity`** *(day-2 — IBM Verify SSO)* — creates two OIDC apps in the IBM Verify
  SaaS tenant via REST and wires **Boundary and Nomad** to trust them, mapping Verify group membership
  → admin / readonly. Additive: the Boundary password admin and Nomad management token remain as
  break-glass logins.
- **`modules/nomad-vault-wif`** *(platform)* — creates the single **`jwt-nomad`** Vault auth method that
  trusts Nomad's workload-identity signing keys (WIF). Per-project WIF roles + read policies live in the
  project tier; this is just the shared trust anchor. The foundation root also enables a **KV-v2** mount
  (`secret/`) where the project tier publishes job templates.
- **GitHub secrets plugin** *(platform)* — the AMI bakes the external
  `martinbaillie/vault-plugin-secrets-github` binary into `/opt/vault/plugins` (sha256-pinned), and
  `vault-github-plugin.tf` registers it into Vault's plugin catalog. It mints short-lived GitHub App
  installation tokens; per-project **mounts** of it (`github/<project>`) are created in the project tier.
  The git push credential for every workspace comes from here — no static PAT anywhere.

## Prerequisites

- AWS credentials in the environment (region defaults to `ap-southeast-1`).
- Packer >= 1.9, Terraform >= 1.7.
- A Boundary Enterprise license **and** a Nomad Enterprise license (the AMI bakes
  `+ent` binaries, which require a license to start).

## Steps

1. **Add your licenses** — Boundary at `config/boundary_license.hclic`, Nomad at
   `config/nomad_license.hclic` and Vault at `config/vault_license.hclic` (replace the placeholders).
   All are gitignored. Vault Enterprise will not start without its license.

2. **Build the AMIs** — the base AMI, **and** the GPU AMI (the GPU node is provisioned
   unconditionally, so its image is a prerequisite — see [GPU worker node](#gpu-worker-node)):
   ```bash
   cd ami/base_image
   packer init .
   packer build -var-file=variables.pkrvars.hcl .          # -> <owner>-boundary-enterprise-<ts>
   cd ../gpu_image
   packer init .
   packer build -var-file=variables.pkrvars.hcl .          # -> <owner>-gpu-workspace-<ts>
   ```
   The base AMI is tagged with the four binary versions; the GPU AMI bakes the NVIDIA driver +
   container toolkit + Nomad + the `nomad-device-nvidia` plugin.

3. **Configure variables:**
   ```bash
   cd ..
   cp terraform.tfvars.example terraform.tfvars
   ```
   Set the base values and the three IBM Verify values (`ibm_verify_tenant`,
   `ibm_verify_api_client_id`, `ibm_verify_api_client_secret`). The `ibm_verify_*`
   variables have **no defaults**, so they must be set before any `terraform`
   plan/apply will run — see [IBM Verify OIDC SSO](#ibm-verify-oidc-sso)
   below for what they are and how to obtain them. (`terraform.tfvars` is
   gitignored — never commit secrets.)

4. **Provision:**
   ```bash
   terraform init
   # Single state: the platform providers (boundary/nomad/vault/restapi) connect to the
   # base over the NLB, so on a fresh stack bring the base up first...
   terraform apply -target=module.secured_codespace
   # ...then a plain apply provisions everything that layers on the now-running base:
   #   module.identity          — IBM Verify OIDC apps + Boundary/Nomad SSO wiring
   #   module.nomad_vault_wif    — the shared jwt-nomad WIF auth method
   #   vault_mount.kv            — KV-v2 mount (secret/) for project job templates
   #   vault_generic_endpoint.github_plugin — register the baked GitHub secrets plugin
   terraform apply
   # Verify the plugin registered + is runnable:
   #   VAULT_ADDR=... VAULT_SKIP_VERIFY=true vault plugin list secret | grep github
   ```
   The first apply finds the just-built AMI via the `<owner>-boundary-enterprise-*`
   filter and waits for cloud-init (Postgres + `boundary database init` + the
   recovery-KMS setup that creates the admin, the **org scope**, password auth
   method and admin role; then the Nomad agent + `nomad acl bootstrap`; then
   `vault operator init`/unseal with the `vault{}` JWT-auth stanza baked into
   `nomad.hcl`), copying the generated IDs to `generated/boundary-setup.json`, the
   Nomad management token to `generated/nomad-setup.json`, and the Vault root token +
   unseal keys to `generated/vault-setup.json`. The second apply fetches an IBM Verify
   token and creates the two OIDC apps (wiring Boundary + Nomad to trust them — see
   the SSO section below), stands up the shared **`jwt-nomad`** Nomad↔Vault WIF auth
   method, and enables the **KV-v2** mount where the project tier publishes job
   templates. The per-project Vault SSH CA, Boundary credential store and WIF role are
   created later by the [project tier](../project/README.md).

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
   ```
   The login name, password and org name are whatever you set in `terraform.tfvars`
   (defaults: `admin` / `Password123!` / `primary-org`). The auth-method and **org
   scope ID** are server-generated and read back from `generated/boundary-setup.json`.
   Project scopes are created later by the [project tier](../project/README.md).

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

## Vault server + Nomad↔Vault WIF

`modules/secured-codespace` runs a single-node **Vault** (file storage, self-signed
TLS) on the all-in-one node, reached at `:8200` through the NLB. The bootstrap
runs `vault operator init` (Shamir, 1 share / 1 threshold for this demo) and
unseals it; the **root token** and **unseal keys** are scp'd back to
`generated/vault-setup.json` and surfaced as the sensitive outputs
`vault_root_token` / `vault_unseal_keys`. The foundation root then adds two shared,
project-agnostic pieces: `modules/nomad-vault-wif` enables the **`jwt-nomad`**
auth method (the **Nomad↔Vault workload-identity** trust anchor — Vault fetches
Nomad's JWKS over HTTPS and validates it with the Nomad CA), and `vault_mount.kv`
enables a **KV-v2** mount at `secret/` for project job templates.

The actual **SSH certificate authority** and **Boundary credential store** are **per
project**, created by the [project tier](../project/README.md) (each project gets its own
`ssh/<project>` CA + `dev-workspace` signing role and a per-project WIF role on this shared
`jwt-nomad` backend). The platform's job here is just the shared trust anchor: the base
`nomad.hcl` carries the `vault{}` stanza for WIF, so this works on a fresh build; rolling it
onto an already-running node needs the in-place `nomad.hcl` update + `systemctl restart
nomad` described in `docs/specs/phase-2-jit-vault-ssh-certs.md`.

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
> plain `terraform apply` to configure the WIF auth method + KV mount. The replace
> rebuilds Boundary/Nomad and re-runs the SSO wiring.

## IBM Verify OIDC SSO

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

## GPU worker node

For GPU workspaces, the platform tier provisions a **second EC2** that joins the existing
all-in-one node as a **Nomad client in the `gpu` node pool** — the main node stays in the
implicit `default` pool, so existing jobs can't drift onto the GPU box and only a flavor that
opts into `node_pool = "gpu"` is placed there.

- **Separate GPU AMI** (`ami/gpu_image/`, built with `packer build` like the base image): an
  Ubuntu 24.04 base with the NVIDIA driver, `nvidia-container-toolkit` (the Docker `nvidia`
  runtime), Nomad `+ent`, and the **`nomad-device-nvidia`** plugin — so the node fingerprints
  its `nvidia/gpu` device and can schedule GPU jobs.
- **`aws_instance.gpu`** (`gpu.tf`, type `var.gpu_instance_type` = `g4dn.xlarge`, root
  `var.gpu_root_volume_size` = 60 GiB) in the same VPC/subnet/SG, with a **public IP** for
  image egress (no NAT in the PoC VPC). It registers to the main node over the VPC; new SG
  rules are **self-referencing/intra-SG only** (Nomad RPC+serf, Vault, demo-db, and the
  Boundary worker → workspace SSH range `2222–2399`) — no new public ingress.
- **Reuses the existing Boundary worker**, which dials the GPU node's private IP over the VPC,
  and the **same Nomad TLS cert** (mutual RPC TLS authorizes the join; a client needs no ACL
  token). No Vault/Boundary/Postgres runs on the GPU node.

The GPU node's private IP surfaces as the `gpu_instance_private_ip` output. The project tier
publishes the `gpu-workspace` flavor (`node_pool = "gpu"`, CUDA image) and the developer tier
(or the portal) places the workspace there — see
[`../project/README.md`](../project/README.md) and
[`../workspace/README.md`](../workspace/README.md#gpu-flavor).

> **The GPU node is currently provisioned unconditionally** — `aws_instance.gpu` has no
> `count`, so a plain `terraform apply` looks up the GPU AMI and creates the GPU instance, and
> its `remote-exec` **blocks the apply** until the node finishes bootstrapping and `nvidia-smi`
> works. So the **GPU AMI (`ami/gpu_image/`) is a prerequisite** of every infra apply (build it
> in step 2 below). To run the base node *without* a GPU box, remove/comment `gpu.tf` and the
> `gpu_*` outputs — making it opt-in (a `count`/feature flag) is a roadmap cleanup.

## Verify the MCP Gateway

The platform tier runs one **ContextForge MCP Gateway** (Nomad ns `infra`, see `mcp-gateway.tf`).
Its admin API is reachable only from the operator `/32` via the NLB on `:4444` (plain HTTP — the
gateway terminates no TLS in the PoC). Auth is **JWT-only** (HTTP Basic is disabled), so every
admin call needs a bearer token minted from the gateway's `JWT_SECRET_KEY`. Mint a short-lived
admin JWT, then list the federated peers, virtual servers, tools, and tokens:

```bash
cd terraform/infra
GW="$(terraform output -raw mcp_gateway_addr)"        # http://<nlb>:4444 (operator /32)

# Signing secret + admin identity from Vault (the gateway KV path; never echoed).
JWT_SECRET="$(vault kv get -mount=secret -field=jwt_secret_key infra/mcp-gateway)"
ADMIN_EMAIL="$(vault kv get -mount=secret -field=admin_email   infra/mcp-gateway)"

# Mint an HS256 admin JWT with the exact claims ContextForge verifies (iss/aud/exp/jti).
# A hand-minted JWT only authenticates the bootstrap admin identity — arbitrary users 401.
b64url() { openssl base64 -e -A | tr '+/' '-_' | tr -d '='; }
now=$(date +%s); exp=$((now + 600)); jti=$(openssl rand -hex 16)
hdr=$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)
pld=$(printf '{"sub":"%s","username":"%s","iss":"mcpgateway","aud":"mcpgateway-api","iat":%s,"exp":%s,"jti":"%s"}' \
  "$ADMIN_EMAIL" "$ADMIN_EMAIL" "$now" "$exp" "$jti" | b64url)
sig=$(printf '%s.%s' "$hdr" "$pld" | openssl dgst -sha256 -hmac "$JWT_SECRET" -binary | b64url)
ADMIN_JWT="$hdr.$pld.$sig"
auth=(-H "Authorization: Bearer $ADMIN_JWT")
```

```bash
# Auth is enforced (Basic disabled): no token => 401, valid token => 200.
curl -s -o /dev/null -w 'no-token: %{http_code}\n' "$GW/health"      # 401
curl -s "${auth[@]}" "$GW/health"; echo                              # {"status":"healthy",...}
curl -s "${auth[@]}" "$GW/version" | jq '{name, version}'            # gateway build

# Peer MCP servers — the federated upstreams (one demo-db-<project> per onboarded project).
curl -s "${auth[@]}" "$GW/gateways" \
  | jq -r '.[] | "\(.id)\t\(.name)\t\(.url)\treachable=\(.reachable // .enabled)"'

# Virtual MCP servers — the per-project tool bundles a workspace token is scoped to.
curl -s "${auth[@]}" "$GW/servers" \
  | jq -r '.[] | "\(.id)\t\(.name)\ttools=\((.associatedTools // .associated_tools) | length)"'

# MCP tools — everything discovered across all peers (each tagged with its peer gateway id).
curl -s "${auth[@]}" "$GW/tools" \
  | jq -r '.[] | "\(.name)\tgateway=\(.gatewayId // .gateway_id)"'

# API tokens — the per-project client tokens (include_inactive shows revoked/soft-deleted ones).
curl -s "${auth[@]}" "$GW/tokens?include_inactive=true&limit=100" \
  | jq -r '.tokens[]? | "\(.id)\t\(.name)\tactive=\(.is_active)"'
```

Notes:
- `/tokens` lists tokens **owned by the caller** (the bootstrap admin, which created the
  per-project client tokens, so they appear here). `/tokens/admin/all` needs full platform-admin
  RBAC and **403s** for a hand-minted JWT — that's expected.
- The per-project client tokens are **server-scoped**: each returns `200` only on its own
  `/servers/<vs>/...` and `403` on every other server/admin endpoint (per-project isolation).
- A virtual server's id is what a workspace connects to: `secret/projects/<project>/mcp` holds
  `{url: <private-endpoint>/servers/<vs-id>/sse, token: <scoped-client-token>}` (project tier).
- The **Admin UI** is the same surface in a browser: open `$GW/` and log in with `admin_email` /
  `admin_password` from `vault kv get -mount=secret infra/mcp-gateway`.

## Next steps

The platform tier is now provisioned. Continue with the lower tiers (each reads this root's
outputs via `terraform_remote_state`, so there is no token/address copying):

- **Project onboarding** *(per project)* → [`../project/README.md`](../project/README.md)
- **Developer workspace** *(per workspace)* → [`../workspace/README.md`](../workspace/README.md)
- **End-to-end developer demo** → [`../README.md`](../README.md#verify-the-boundary-brokered-workspace-ssh)

## Notes

- AEAD keys are generated once by Terraform (`random_bytes`) and held in state, so
  they remain **stable across reboots** — required, since rotating them would orphan
  the DB-encrypted data.
- The admin account, **org scope**, password auth method and admin role are
  created on the instance during cloud-init via Boundary's **recovery-KMS workflow**
  (no controller login required — it authenticates with the recovery AEAD key). This
  runs locally against `127.0.0.1:9200`, so it does not depend on the NLB being healthy.
  Login name, password and the org name come straight from `terraform.tfvars`; project
  scopes are created later by the [project tier](../project/README.md).
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
