# Vault credential store in the Boundary project scope. Boundary's controller
# authenticates to Vault with the dedicated periodic token (NOT root) and will
# self-renew it. Phase 2 targets attach credential libraries to this store to
# broker workspace SSH credentials.
resource "boundary_credential_store_vault" "vault" {
  name        = "vault"
  description = "Vault credential store (dedicated least-privilege token)"
  scope_id    = var.project_scope_id

  address         = var.vault_cred_store_address
  token           = vault_token.boundary.client_token
  tls_skip_verify = true # base uses a self-signed cert
}
