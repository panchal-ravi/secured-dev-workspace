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
├── providers.tf  variables.tf  main.tf  outputs.tf  terraform.tfvars
└── generated/                 # runtime artifacts (SSH key, init JSON) — gitignored
```

Everything lives in a **single Terraform state**: the base node (`modules/secured-codespace`,
which now also runs Vault), the IBM Verify SSO layer (`modules/identity`) and the Boundary Vault
credential store (`modules/credential-store-vault`). All module calls are in `main.tf`; all provider
configs are in `providers.tf`.

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
   # Single state: the identity providers connect to the base over the NLB, so on
   # a fresh stack bring the base up first...
   terraform apply -target=module.secured_codespace
   # ...then a plain apply provisions module.identity — the IBM Verify OIDC apps +
   # the Boundary/Nomad SSO wiring — on top of the now-running base.
   terraform apply
   ```
   The first apply finds the just-built AMI via the `<owner>-boundary-enterprise-*`
   filter and waits for cloud-init (Postgres + `boundary database init` + the
   recovery-KMS setup that creates the admin, org/project scopes, password auth
   method and admin role; then the Nomad agent + `nomad acl bootstrap`), copying the
   generated IDs to `generated/boundary-setup.json` and the Nomad management token to
   `generated/nomad-setup.json`. The second apply fetches an IBM Verify token,
   creates the two OIDC apps, and wires Boundary + Nomad to trust them (see the SSO
   section below).

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
Phase 4 groundwork; Phase 2 targets will attach credential libraries to the store.

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
