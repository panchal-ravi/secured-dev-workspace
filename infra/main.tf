module "secured_codespace" {
  source = "./modules/secured-codespace"

  owner            = var.owner
  region           = var.region
  instance_type    = var.instance_type
  boundary_version = var.boundary_version
  boundary_license = file("${path.root}/config/boundary_license.hclic")

  nomad_version = var.nomad_version
  nomad_license = file("${path.root}/config/nomad_license.hclic")

  vault_version = var.vault_version
  vault_license = file("${path.root}/config/vault_license.hclic")

  boundary_admin_login_name = var.boundary_admin_login_name
  boundary_admin_password   = var.boundary_admin_password
  boundary_org_name         = var.boundary_org_name
  boundary_project_name     = var.boundary_project_name
}

# Zero-downtime state migration after renaming the module
# boundary-allinone -> secured-codespace. Lets `terraform apply` move the
# existing resources instead of destroying and recreating them.
moved {
  from = module.boundary_allinone
  to   = module.secured_codespace
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
}

# Vault credential-store layer: registers the base node's Vault as a credential
# store in the Boundary project scope, authenticated with a dedicated
# least-privilege periodic token (not root). Its providers (vault/boundary) are
# configured in providers.tf. Like module.identity, it talks only to the
# already-running Vault/Boundary over the NLB — it never touches the base instance.
module "credential_store_vault" {
  source = "./modules/credential-store-vault"

  depends_on = [module.secured_codespace]

  project_scope_id = module.secured_codespace.project_scope_id
}
