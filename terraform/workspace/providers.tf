# Provider configuration for the developer tier. All connection inputs come from
# the foundation state (local.f, defined in main.tf). The base node uses a
# self-signed cert behind the NLB, so TLS verification is skipped.
provider "nomad" {
  address     = local.f.nomad_addr
  skip_verify = true
  secret_id   = local.f.nomad_management_token
}

provider "boundary" {
  addr                   = local.f.boundary_addr
  tls_insecure           = true
  auth_method_id         = local.f.admin_auth_method_id
  auth_method_login_name = local.f.admin_login_name
  auth_method_password   = local.f.admin_password
}

provider "vault" {
  address         = local.f.vault_addr
  token           = local.f.vault_root_token
  skip_tls_verify = true
}
