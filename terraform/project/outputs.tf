# Consumed by the dev-workspace tier (one project's workspaces read these via
# terraform_remote_state of THIS project's state).
output "project_scope_id" {
  description = "Boundary project scope id (workspace targets/roles/aliases live under this)."
  value       = boundary_scope.project.id
}

output "namespace" {
  description = "Nomad namespace for this project (= project_name)."
  value       = nomad_namespace.project.name
}

output "credential_store_id" {
  description = "Boundary Vault credential store id in the project scope."
  value       = boundary_credential_store_vault.this.id
}

output "credential_library_id" {
  description = "Boundary Vault SSH-certificate credential library id — every workspace target in this project injects it."
  value       = boundary_credential_library_vault_ssh_certificate.this.id
}

output "ssh_mount_path" {
  description = "Vault SSH secrets engine mount path for this project (`ssh/<project>`)."
  value       = vault_mount.ssh.path
}

output "ssh_sign_path" {
  description = "Vault path Boundary signs SSH certs against for this project."
  value       = "${vault_mount.ssh.path}/sign/${vault_ssh_secret_backend_role.dev_workspace.name}"
}

output "ssh_ca_path" {
  description = "Vault path the workspace job's WIF template reads the project SSH CA public key from."
  value       = "${vault_mount.ssh.path}/config/ca"
}

output "ssh_ca_public_key" {
  description = "Project SSH CA public key (installed as the workspace sshd TrustedUserCAKeys)."
  value       = vault_ssh_secret_backend_ca.ssh.public_key
}

output "wif_role" {
  description = "Nomad↔Vault WIF role name on the shared jwt-nomad backend (= project_name); the workspace job names it in its vault{} stanza."
  value       = vault_jwt_auth_backend_role.project.role_name
}

output "job_template_names" {
  description = "Names of the job templates published to Vault KV for this project."
  value       = keys(vault_kv_secret_v2.job_template)
}

output "job_template_node_pools" {
  description = "Map of published job-template name => its Nomad node pool (\"\" = the implicit default pool). The developer tier reads the selected flavor's pool to place the home volume and the Boundary host on the right node (e.g. \"gpu\")."
  value       = { for name, cfg in var.workspace_templates : name => cfg.node_pool }
}

output "github_token_path" {
  description = "Vault path the workspace WIF task reads a pre-scoped, short-lived GitHub App token from (github/<project>/token/<permissionset>)."
  value       = "${vault_mount.github.path}/token/${local.github_permissionset_name}"
}

output "db_creds_path" {
  description = "Vault path the workspace WIF task reads a per-session, read-only DB credential from (database/<project>/creds/dev-workspace-ro)."
  value       = "${vault_mount.database.path}/creds/${vault_database_secret_backend_role.dev_workspace_ro.name}"
}

output "db_endpoint" {
  description = "host:port the workspace container uses to reach the demo-db (node private IP + the demo-db static port). Distinct from Vault's 127.0.0.1 connection."
  value       = "${local.f.instance_private_ip}:${local.demo_db_port}"
}

output "deepseek_key_path" {
  description = "Vault path the workspace WIF task reads the DeepSeek API key from (KV v2, so /data/ prefixed: secret/data/projects/<project>/deepseek)."
  value       = "${local.f.kv_mount_path}/data/projects/${var.project_name}/deepseek"
}
