module "secured_codespace" {
  source = "./modules/secured-codespace"

  owner                = var.owner
  region               = var.region
  instance_type        = var.instance_type
  enable_gpu_node      = var.enable_gpu_node
  gpu_instance_type    = var.gpu_instance_type
  gpu_root_volume_size = var.gpu_root_volume_size

  enable_agent_nodes     = var.enable_agent_nodes
  agent_node_count       = var.agent_node_count
  agent_instance_type    = var.agent_instance_type
  agent_root_volume_size = var.agent_root_volume_size

  enable_default_spare = var.enable_default_spare

  enable_shared_volume = var.enable_shared_volume

  enable_microvm_node      = var.enable_microvm_node
  microvm_instance_type    = var.microvm_instance_type
  microvm_root_volume_size = var.microvm_root_volume_size

  boundary_version = var.boundary_version
  boundary_license = file("${path.root}/config/boundary_license.hclic")

  nomad_version = var.nomad_version
  nomad_license = file("${path.root}/config/nomad_license.hclic")

  vault_version = var.vault_version
  vault_license = file("${path.root}/config/vault_license.hclic")

  boundary_admin_login_name = var.boundary_admin_login_name
  boundary_admin_password   = var.boundary_admin_password
  boundary_org_name         = var.boundary_org_name
}

# Identity layer: IBM Verify OIDC SSO for Boundary + Nomad (roadmap Phase 6).
# Folded into the root state so the whole stack lives in a single Terraform
# state file. The module never touches the frozen base instance — it only talks
# to the Verify REST API and the already-running Boundary/Nomad over the NLB.
# Its providers (restapi/boundary/nomad) are configured in providers.tf.
module "identity" {
  source = "./modules/identity"

  depends_on = [module.secured_codespace]

  ibm_verify_tenant   = var.ibm_verify_tenant
  verify_access_token = local.verify_access_token
  boundary_addr       = module.secured_codespace.boundary_addr
  nomad_addr          = module.secured_codespace.nomad_addr
  admin_group_name    = var.admin_group_name
  readonly_group_name = var.readonly_group_name

  # Developer Portal OIDC app — created here (not hand-registered) only when the
  # portal is deployed. The redirect URI is derived from the live NLB.
  create_portal_app   = var.enable_developer_portal
  portal_redirect_url = local.portal_redirect_url
  portal_audiences    = var.portal_oidc_audiences
}

# Platform-tier Nomad↔Vault workload-identity federation: the single jwt-nomad
# auth method that trusts Nomad's workload-identity signing keys. Per-project SSH
# CAs, signing roles, Boundary credential stores and the per-project WIF read
# roles are created by the project-onboarding tier (modules/project), not here.
# Uses the default vault provider (providers.tf, root token over the NLB) — it
# only talks to the already-running Vault, never the base instance.
module "nomad_vault_wif" {
  source = "./modules/nomad-vault-wif"

  depends_on = [module.secured_codespace]

  nomad_ca_pem = module.secured_codespace.nomad_ca_pem
}
