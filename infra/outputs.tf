output "boundary_addr" {
  description = "Boundary controller API address (via the NLB)"
  value       = module.secured_codespace.boundary_addr
}

output "boundary_worker_proxy_addr" {
  description = "Boundary worker proxy address (via the NLB)"
  value       = module.secured_codespace.boundary_worker_proxy_addr
}

output "nlb_dns_name" {
  description = "Public NLB DNS name"
  value       = module.secured_codespace.nlb_dns_name
}

output "instance_public_ip" {
  description = "Public IP of the all-in-one instance (SSH)"
  value       = module.secured_codespace.instance_public_ip
}

output "boundary_oidc_login_command" {
  description = "Ready-to-run Boundary SSO login"
  value       = module.identity.boundary_login_command
}

output "nomad_oidc_login_command" {
  description = "Ready-to-run Nomad SSO login"
  value       = module.identity.nomad_login_command
}

output "nomad_addr" {
  description = "Nomad HTTP API address (via the NLB)"
  value       = module.secured_codespace.nomad_addr
}

output "nomad_ui_addr" {
  description = "Nomad web UI address (via the NLB)"
  value       = module.secured_codespace.nomad_ui_addr
}

output "nomad_management_token" {
  description = "Nomad ACL bootstrap management token (SecretID)"
  value       = module.secured_codespace.nomad_management_token
  sensitive   = true
}

output "vault_addr" {
  description = "Vault API address (via the NLB)"
  value       = module.secured_codespace.vault_addr
}

output "vault_root_token" {
  description = "Vault initial root token"
  value       = module.secured_codespace.vault_root_token
  sensitive   = true
}

output "vault_unseal_keys" {
  description = "Vault Shamir unseal keys (base64)"
  value       = module.secured_codespace.vault_unseal_keys
  sensitive   = true
}

output "vault_credential_store_id" {
  description = "Boundary Vault credential store ID (project scope)"
  value       = module.credential_store_vault.credential_store_id
}

/*
output "allowed_ingress_cidr" {
  description = "The single /32 allowed onto every exposed port (the caller's public IP)"
  value       = module.secured_codespace.allowed_ingress_cidr
}

output "ssh_private_key_path" {
  description = "Path to the generated SSH private key"
  value       = module.secured_codespace.ssh_private_key_path
}

output "admin_auth_method_id" {
  description = "Primary password auth-method ID"
  value       = module.secured_codespace.admin_auth_method_id
}

output "admin_login_name" {
  description = "Admin login name"
  value       = module.secured_codespace.admin_login_name
}

output "admin_password" {
  description = "Admin password"
  value       = module.secured_codespace.admin_password
  sensitive   = true
}

output "org_scope_id" {
  description = "ID of the org scope created under global"
  value       = module.secured_codespace.org_scope_id
}

output "org_name" {
  description = "Name of the org scope"
  value       = module.secured_codespace.org_name
}

output "project_scope_id" {
  description = "ID of the project scope created under the org"
  value       = module.secured_codespace.project_scope_id
}

output "project_name" {
  description = "Name of the project scope"
  value       = module.secured_codespace.project_name
}

output "nomad_token_accessor_id" {
  description = "Accessor ID of the Nomad management token"
  value       = module.secured_codespace.nomad_token_accessor_id
}

# --- Identity layer: IBM Verify OIDC SSO ---

output "boundary_oidc_auth_method_id" {
  description = "Boundary OIDC auth-method ID (use with `boundary authenticate oidc`)"
  value       = module.identity.boundary_oidc_auth_method_id
}

output "nomad_oidc_auth_method_name" {
  description = "Nomad OIDC auth-method name (use with `nomad login -method=`)"
  value       = module.identity.nomad_oidc_auth_method_name
}

output "boundary_app_client_id" {
  description = "IBM Verify OIDC client ID for the Boundary application"
  value       = module.identity.boundary_app_client_id
}

output "nomad_app_client_id" {
  description = "IBM Verify OIDC client ID for the Nomad application"
  value       = module.identity.nomad_app_client_id
}

*/
