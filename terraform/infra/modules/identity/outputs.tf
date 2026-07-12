output "boundary_oidc_auth_method_id" {
  description = "Boundary OIDC auth-method ID (use with `boundary authenticate oidc`)"
  value       = boundary_auth_method_oidc.ibm_verify.id
}

output "nomad_oidc_auth_method_name" {
  description = "Nomad OIDC auth-method name (use with `nomad login -method=`)"
  value       = nomad_acl_auth_method.ibm_verify.name
}

output "boundary_app_client_id" {
  description = "IBM Verify OIDC client ID for the Boundary application"
  value       = local.boundary_client_id
}

output "nomad_app_client_id" {
  description = "IBM Verify OIDC client ID for the Nomad application"
  value       = local.nomad_client_id
}

output "portal_app_client_id" {
  description = "IBM Verify OIDC client ID for the Developer Portal app (null when create_portal_app = false)"
  value       = local.portal_client_id
}

output "portal_app_client_secret" {
  description = "IBM Verify OIDC client secret for the Developer Portal app (null when create_portal_app = false)"
  value       = local.portal_client_secret
  sensitive   = true
}

output "boundary_login_command" {
  description = "Ready-to-run Boundary SSO login"
  value       = "boundary authenticate oidc -addr ${local.boundary_addr} -auth-method-id ${boundary_auth_method_oidc.ibm_verify.id} -tls-insecure"
}

output "nomad_login_command" {
  description = "Ready-to-run Nomad SSO login"
  value       = "nomad login -address=${local.nomad_addr} -method=${nomad_acl_auth_method.ibm_verify.name}"
}
