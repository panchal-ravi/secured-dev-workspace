output "boundary_addr" {
  description = "Boundary controller API address (via the NLB)"
  value       = module.boundary_allinone.boundary_addr
}

output "boundary_worker_proxy_addr" {
  description = "Boundary worker proxy address (via the NLB)"
  value       = module.boundary_allinone.boundary_worker_proxy_addr
}

output "nlb_dns_name" {
  description = "Public NLB DNS name"
  value       = module.boundary_allinone.nlb_dns_name
}

output "instance_public_ip" {
  description = "Public IP of the all-in-one instance (SSH)"
  value       = module.boundary_allinone.instance_public_ip
}

output "allowed_ingress_cidr" {
  description = "The single /32 allowed onto every exposed port (the caller's public IP)"
  value       = module.boundary_allinone.allowed_ingress_cidr
}

output "ssh_private_key_path" {
  description = "Path to the generated SSH private key"
  value       = module.boundary_allinone.ssh_private_key_path
}

output "admin_auth_method_id" {
  description = "Primary password auth-method ID"
  value       = module.boundary_allinone.admin_auth_method_id
}

output "admin_login_name" {
  description = "Admin login name"
  value       = module.boundary_allinone.admin_login_name
}

output "admin_password" {
  description = "Admin password"
  value       = module.boundary_allinone.admin_password
  sensitive   = true
}

output "org_scope_id" {
  description = "ID of the org scope created under global"
  value       = module.boundary_allinone.org_scope_id
}

output "org_name" {
  description = "Name of the org scope"
  value       = module.boundary_allinone.org_name
}

output "project_scope_id" {
  description = "ID of the project scope created under the org"
  value       = module.boundary_allinone.project_scope_id
}

output "project_name" {
  description = "Name of the project scope"
  value       = module.boundary_allinone.project_name
}
