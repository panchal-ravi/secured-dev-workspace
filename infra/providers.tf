terraform {
  required_version = ">= 1.7"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.38"
    }
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
    cloudinit = {
      source  = "hashicorp/cloudinit"
      version = "~> 2.3"
    }
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
    nomad = {
      source  = "hashicorp/nomad"
      version = "~> 2.4"
    }
    restapi = {
      source  = "Mastercard/restapi"
      version = "~> 1.20"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# ---------------------------------------------------------------------------
# Identity layer providers (IBM Verify OIDC SSO, roadmap Phase 6).
#
# These are configured from the base module's outputs and from a live Verify
# bootstrap token, and are inherited by ./modules/identity automatically
# (default, unaliased configs). TRADEOFF of the single state: EVERY
# `terraform plan`/`apply` on this root now (a) fetches a Verify token (so the
# ibm_verify_* variables must be populated and the tenant reachable) and (b)
# connects to Boundary and Nomad (so the base instance must already be applied
# and running). Apply the base first; the identity resources then come up on a
# subsequent apply. See infra/README.md.
# ---------------------------------------------------------------------------

locals {
  verify_tenant_url = "https://${var.ibm_verify_tenant}"
  verify_token_url  = "${local.verify_tenant_url}/v1.0/endpoint/default/token"
}

# Bootstrap access token for the IBM Verify management API (OAuth2
# client-credentials grant using the manually-provisioned API client).
# Short-lived; obtained at plan time and used for this apply.
data "http" "verify_token" {
  url    = local.verify_token_url
  method = "POST"

  request_headers = {
    Content-Type = "application/x-www-form-urlencoded"
    Accept       = "application/json"
  }

  request_body = join("&", [
    "grant_type=client_credentials",
    "client_id=${var.ibm_verify_api_client_id}",
    "client_secret=${var.ibm_verify_api_client_secret}",
  ])
}

locals {
  verify_access_token = jsondecode(data.http.verify_token.response_body).access_token
}

provider "restapi" {
  uri = local.verify_tenant_url
  # Verify's POST returns an `_links` stub; the provider parses it to learn the
  # app id (from _links/self/href), then GETs the full object on read to surface
  # clientId/clientSecret. Must be true (see modules/identity/verify.tf).
  write_returns_object = true

  headers = {
    Authorization = "Bearer ${local.verify_access_token}"
    Content-Type  = "application/json"
    Accept        = "application/json"
  }
}

provider "boundary" {
  addr                   = module.secured_codespace.boundary_addr
  tls_insecure           = true # base uses a self-signed cert behind the NLB
  auth_method_id         = module.secured_codespace.admin_auth_method_id
  auth_method_login_name = module.secured_codespace.admin_login_name
  auth_method_password   = module.secured_codespace.admin_password
}

provider "nomad" {
  address     = module.secured_codespace.nomad_addr
  skip_verify = true # base uses a self-signed cert behind the NLB
  secret_id   = module.secured_codespace.nomad_management_token
}

# Vault provider for the Boundary credential-store layer. Reaches Vault through
# the NLB with the initial root token (from the base module). Same single-state
# tradeoff as above: EVERY plan/apply now needs Vault reachable AND UNSEALED.
# With Shamir unseal, Vault comes back sealed after a reboot — re-unseal it
# (see infra/README.md) before running terraform.
provider "vault" {
  address         = module.secured_codespace.vault_addr
  token           = module.secured_codespace.vault_root_token
  skip_tls_verify = true # base uses a self-signed cert behind the NLB
}
