terraform {
  required_version = ">= 1.7"

  required_providers {
    nomad = {
      source  = "hashicorp/nomad"
      version = "~> 2.6" # 2.5+ for the dynamic-host-volume resource
    }
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}

# ---------------------------------------------------------------------------
# Day-2 workspace layer providers. This is a SEPARATE Terraform state from the
# base infra/ root: it only talks to the already-running Nomad + Boundary over
# the NLB, so it re-applies without ever touching the base instance.
#
# Inputs come from the base root's outputs — populate terraform.tfvars from:
#   terraform -chdir=../infra output -raw nomad_addr
#   terraform -chdir=../infra output -raw boundary_addr
#   terraform -chdir=../infra output -raw admin_login_name / admin_password
#   terraform -chdir=../infra output -raw boundary_oidc_auth_method_id
#   terraform -chdir=../infra output -raw project_scope_id
# The Nomad management token is read straight from the file the base apply
# wrote (generated/nomad-setup.json), so it never has to be copied by hand.
# ---------------------------------------------------------------------------

locals {
  nomad_management_token = jsondecode(file(var.nomad_token_path)).management_token
}

provider "nomad" {
  address     = var.nomad_addr
  skip_verify = true # base uses a self-signed cert behind the NLB
  secret_id   = local.nomad_management_token
}

provider "boundary" {
  addr                   = var.boundary_addr
  tls_insecure           = true # base uses a self-signed cert behind the NLB
  auth_method_id         = var.boundary_admin_auth_method_id
  auth_method_login_name = var.boundary_admin_login
  auth_method_password   = var.boundary_admin_password
}
