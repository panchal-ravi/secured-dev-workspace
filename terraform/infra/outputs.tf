# ---------------------------------------------------------------------------
# Platform-tier (foundation) outputs. The project and developer tiers
# (terraform/project/, terraform/workspace/) consume these automatically via
# `terraform_remote_state` of this root's state (../infra/terraform.tfstate) —
# no manual copying. Secrets are marked sensitive; read them with
# `terraform output -raw <name>`.
# ---------------------------------------------------------------------------

# --- Connection addresses (via the NLB) ---

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

output "instance_private_ip" {
  description = "Private IP of the all-in-one node. The co-located Boundary worker dials workspace SSH host ports here (Nomad's docker driver publishes them on this IP) — the workspace tier's workspace_host_address."
  value       = module.secured_codespace.instance_private_ip
}

output "nomad_addr" {
  description = "Nomad HTTP API address (via the NLB)"
  value       = module.secured_codespace.nomad_addr
}

output "gpu_instance_private_ip" {
  description = "Private IP of the GPU worker node, or null when enable_gpu_node = false. The developer tier (terraform/workspace) points the Boundary host at this for a GPU flavor; the portal resolves runtime placement from the alloc's node attribute."
  value       = module.secured_codespace.gpu_instance_private_ip
}

output "gpu_instance_public_ip" {
  description = "Public IP of the GPU worker node (direct SSH for operator troubleshooting), or null when enable_gpu_node = false."
  value       = module.secured_codespace.gpu_instance_public_ip
}

output "nomad_ui_addr" {
  description = "Nomad web UI address (via the NLB)"
  value       = module.secured_codespace.nomad_ui_addr
}

output "vault_addr" {
  description = "Vault API address (via the NLB)"
  value       = module.secured_codespace.vault_addr
}

# --- Boundary admin + org scope (project scopes are created per-project) ---

output "admin_auth_method_id" {
  description = "Primary password auth-method ID (downstream provider admin auth)"
  value       = module.secured_codespace.admin_auth_method_id
}

output "admin_login_name" {
  description = "Boundary admin login name"
  value       = module.secured_codespace.admin_login_name
}

output "admin_password" {
  description = "Boundary admin password"
  value       = module.secured_codespace.admin_password
  sensitive   = true
}

output "org_scope_id" {
  description = "ID of the org scope under global (project scopes are created beneath it per-project)"
  value       = module.secured_codespace.org_scope_id
}

output "org_name" {
  description = "Name of the org scope"
  value       = module.secured_codespace.org_name
}

# --- IBM Verify OIDC SSO (identity tier) ---

output "boundary_oidc_login_command" {
  description = "Ready-to-run Boundary SSO login"
  value       = module.identity.boundary_login_command
}

output "nomad_oidc_login_command" {
  description = "Ready-to-run Nomad SSO login"
  value       = module.identity.nomad_login_command
}

output "boundary_oidc_auth_method_id" {
  description = "Boundary OIDC auth-method ID (per-developer managed groups attach here)"
  value       = module.identity.boundary_oidc_auth_method_id
}

output "nomad_oidc_auth_method_name" {
  description = "Nomad OIDC auth-method name (project namespace binding rules attach here)"
  value       = module.identity.nomad_oidc_auth_method_name
}

# --- Tokens + unseal material (sensitive) ---

output "nomad_management_token" {
  description = "Nomad ACL bootstrap management token (SecretID)"
  value       = module.secured_codespace.nomad_management_token
  sensitive   = true
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

# --- Per-project wiring inputs (project tier) ---

output "jwt_backend_path" {
  description = "Vault jwt-nomad auth method path (project WIF read-roles attach to this backend)"
  value       = module.nomad_vault_wif.backend_path
}

output "kv_mount_path" {
  description = "Vault KV-v2 mount path where project job templates are stored"
  value       = vault_mount.kv.path
}

output "nomad_ca_pem" {
  description = "Nomad self-signed TLS cert (its own CA) — for Vault JWKS validation in Nomad↔Vault WIF"
  value       = module.secured_codespace.nomad_ca_pem
}
