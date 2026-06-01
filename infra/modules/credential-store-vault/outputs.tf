output "credential_store_id" {
  description = "ID of the Vault credential store in the Boundary project scope"
  value       = boundary_credential_store_vault.vault.id
}
