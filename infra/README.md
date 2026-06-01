# Boundary Enterprise — all-in-one on AWS

Provisions **HashiCorp Boundary Enterprise** as a single all-in-one node on AWS:

1. A **Packer** base image (`ami/base_image/`) that bakes the Boundary, Consul,
   Nomad and Vault Enterprise binaries onto the org base AMI.
2. A **Terraform** module (`modules/boundary-allinone/`) that launches one EC2
   instance from that AMI running the Boundary **controller + worker** on the same
   host, backed by a **local PostgreSQL**, using static **AEAD KMS keys** (root /
   worker-auth / recovery).

The controller API (`9200`) and worker proxy (`9202`) are reached through a public
**Network Load Balancer**. SSH is direct to the instance. **All ingress — the NLB
listeners and SSH — is locked to the public IP of the machine running Terraform
(`/32`)**, auto-detected via `https://checkip.amazonaws.com`.

## Layout

```
infra/
├── ami/base_image/            # Packer: builds <owner>-boundary-enterprise-* AMI
├── config/                    # Boundary HCL template, systemd unit, license
├── modules/boundary-allinone/ # VPC, NLB, SGs, TLS, EC2, bootstrap
├── providers.tf  variables.tf  main.tf  outputs.tf  terraform.tfvars
└── generated/                 # runtime artifacts (SSH key, init JSON) — gitignored
```

## Prerequisites

- AWS credentials in the environment (region defaults to `ap-southeast-1`).
- Packer >= 1.9, Terraform >= 1.7.
- A Boundary Enterprise license.

## Steps

1. **Add your license** at `config/boundary_license.hclic` (replace the placeholder).

2. **Build the AMI:**
   ```bash
   cd ami/base_image
   packer init .
   packer build -var-file=variables.pkrvars.hcl .
   ```
   Produces `<owner>-boundary-enterprise-<timestamp>`, tagged with the four versions.

3. **Provision:**
   ```bash
   cd ..
   terraform init
   terraform apply
   ```
   The module finds the just-built AMI via the `<owner>-boundary-enterprise-*` filter,
   waits for cloud-init (Postgres + `boundary database init` + the recovery-KMS setup
   that creates the admin, org/project scopes, password auth method and admin role),
   and copies the generated IDs to `generated/boundary-setup.json`.

4. **Retrieve admin credentials and scope IDs:**
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

5. **Connect** (self-signed API cert, so skip TLS verification):
   ```bash
   boundary authenticate password \
     -addr "$(terraform output -raw boundary_addr)" \
     -auth-method-id "$(terraform output -raw admin_auth_method_id)" \
     -login-name "$(terraform output -raw admin_login_name)" \
     -tls-insecure
   ```
   (Enter the configured `boundary_admin_password` when prompted.)

## Notes

- AEAD keys are generated once by Terraform (`random_bytes`) and held in state, so
  they remain **stable across reboots** — required, since rotating them would orphan
  the DB-encrypted data.
- The admin account, org/project scopes, password auth method and admin role are
  created on the instance during cloud-init via Boundary's **recovery-KMS workflow**
  (no controller login required — it authenticates with the recovery AEAD key). This
  runs locally against `127.0.0.1:9200`, so it does not depend on the NLB being healthy.
  Login name, password and the org/project names come straight from `terraform.tfvars`.
- `generated/` holds the SSH private key and `boundary-setup.json` (server-generated
  scope/auth-method/user/role IDs — no secrets); it is gitignored and the setup file
  is removed on `terraform destroy`. The admin password is a Terraform variable, not a
  file artifact.
- Scope is a single demo node: no AWS KMS, no RDS, no separate controller/worker
  split, no bastion. The `hashicorp/boundary` provider (targets/host-catalogs) is out
  of scope; the initial scopes/auth-method/admin/role are provisioned via the CLI
  recovery workflow above instead.
- Change the owner, region, instance type, or binary versions in `terraform.tfvars`
  and `ami/base_image/variables.pkrvars.hcl`.
