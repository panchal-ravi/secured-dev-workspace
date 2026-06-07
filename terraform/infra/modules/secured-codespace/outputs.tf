locals {
  # The bootstrap writes the generated scope / auth-method / user / role IDs to
  # boundary-setup.json (scp'd back into ./generated). No secrets are in it.
  setup = try(jsondecode(data.local_file.boundary_setup.content), {})
}

output "boundary_addr" {
  description = "Boundary controller API address (via the NLB)"
  value       = "https://${aws_lb.this.dns_name}:9200"
}

output "boundary_worker_proxy_addr" {
  description = "Boundary worker proxy address (via the NLB)"
  value       = "${aws_lb.this.dns_name}:9202"
}

output "nlb_dns_name" {
  description = "Public NLB DNS name"
  value       = aws_lb.this.dns_name
}

output "instance_public_ip" {
  description = "Public IP of the all-in-one instance (SSH)"
  value       = aws_instance.this.public_ip
}

output "instance_private_ip" {
  description = "Private IP of the all-in-one node — Nomad's docker driver publishes workspace SSH host ports here and the co-located Boundary worker dials it (the workspace tier's workspace_host_address)."
  value       = aws_instance.this.private_ip
}

output "allowed_ingress_cidr" {
  description = "The single /32 allowed onto every exposed port (the caller's public IP)"
  value       = local.allowed_cidr
}

output "vpc_cidr" {
  description = "CIDR of the dedicated VPC. The MCP gateway allowlists this for SSRF so it may federate per-project MCP peers running on the node-private network."
  value       = var.vpc_cidr
}

output "gpu_instance_private_ip" {
  description = "Private IP of the GPU worker node, or null when enable_gpu_node = false. The developer tier (terraform/workspace) points the Boundary host at this for a GPU flavor; the portal instead resolves the placement IP dynamically from the alloc's node attribute."
  value       = one(aws_instance.gpu[*].private_ip)
}

output "gpu_instance_public_ip" {
  description = "Public IP of the GPU worker node (direct SSH for operator troubleshooting; image egress), or null when enable_gpu_node = false."
  value       = one(aws_instance.gpu[*].public_ip)
}

output "ssh_private_key_path" {
  description = "Path to the generated SSH private key"
  value       = "${path.root}/generated/${local.ssh_key_filename}"
}

output "admin_auth_method_id" {
  description = "Primary password auth-method ID"
  value       = try(local.setup.auth_method_id, null)
}

output "admin_login_name" {
  description = "Admin login name"
  value       = var.boundary_admin_login_name
}

output "admin_password" {
  description = "Admin password"
  value       = var.boundary_admin_password
  sensitive   = true
}

output "org_scope_id" {
  description = "ID of the org scope created under global (project scopes are created beneath it per-project)"
  value       = try(local.setup.org_scope_id, null)
}

output "org_name" {
  description = "Name of the org scope"
  value       = var.boundary_org_name
}

# ---------------------------------------------------------------------------
# Nomad (single combined server + client) connection details.
# nomad-setup.json holds the ACL bootstrap management token (scp'd to ./generated).
# ---------------------------------------------------------------------------
output "nomad_addr" {
  description = "Nomad HTTP API address (via the NLB)"
  value       = "https://${aws_lb.this.dns_name}:4646"
}

output "nomad_ui_addr" {
  description = "Nomad web UI address (via the NLB)"
  value       = "https://${aws_lb.this.dns_name}:4646/ui"
}

output "nomad_management_token" {
  description = "Nomad ACL bootstrap management token (SecretID)"
  value       = try(jsondecode(data.local_file.nomad_setup.content).management_token, null)
  sensitive   = true
}

output "nomad_token_accessor_id" {
  description = "Accessor ID of the Nomad management token"
  value       = try(jsondecode(data.local_file.nomad_setup.content).accessor_id, null)
}

# ---------------------------------------------------------------------------
# Vault (single-node) connection details.
# vault-setup.json holds the root token + Shamir unseal keys (scp'd to ./generated).
# ---------------------------------------------------------------------------
output "vault_addr" {
  description = "Vault API address (via the NLB)"
  value       = "https://${aws_lb.this.dns_name}:8200"
}

output "vault_root_token" {
  description = "Vault initial root token"
  value       = try(jsondecode(data.local_file.vault_setup.content).root_token, null)
  sensitive   = true
}

output "vault_unseal_keys" {
  description = "Vault Shamir unseal keys (base64)"
  value       = try(jsondecode(data.local_file.vault_setup.content).unseal_keys, null)
  sensitive   = true
}

output "nomad_ca_pem" {
  description = "Nomad self-signed TLS cert (its own CA) — for Vault JWKS validation in Nomad↔Vault WIF"
  value       = tls_self_signed_cert.nomad.cert_pem
}
