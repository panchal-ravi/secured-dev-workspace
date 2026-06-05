# ---------------------------------------------------------------------------
# Project-tier root (flat — no child module). Onboards ONE project across all
# three control planes: a Boundary project scope (boundary.tf), a Nomad
# namespace + ACL (nomad.tf), a per-project Vault SSH CA + WIF role + Boundary
# credential-store token (vault.tf), and the project's Nomad job templates in
# Vault KV (kv.tf). Reuse ONE Terraform workspace per project
# (`terraform workspace new <project>`); each gets its own state.
#
# Connection + identity inputs are read straight from the FOUNDATION state
# (../infra/terraform.tfstate) via terraform_remote_state (local.f), so there is
# no manual token/address copying — the foundation must be applied first. Vault
# must be UNSEALED (this tier creates Vault mounts/policies/tokens).
# ---------------------------------------------------------------------------
data "terraform_remote_state" "foundation" {
  backend = "local"
  config = {
    path = "${path.module}/../infra/terraform.tfstate"
  }
}

locals {
  f = data.terraform_remote_state.foundation.outputs
}
