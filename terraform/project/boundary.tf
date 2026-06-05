# ---------------------------------------------------------------------------
# Boundary project scope + Vault credential store/library for this project.
# Created once at onboarding; every workspace target in the project references
# the single credential library (per-developer isolation lives in the
# dev-workspace tier's target/role/managed-group graph, not here).
# ---------------------------------------------------------------------------

# Project scope under the foundation's org scope. auto_create_admin_role gives the
# creating admin manage rights on the new scope (the recovery-KMS admin role's
# this,descendants grant on global already covers it; this is belt-and-suspenders).
resource "boundary_scope" "project" {
  name                   = var.project_name
  description            = "Project scope for ${var.project_name}"
  scope_id               = local.f.org_scope_id
  auto_create_admin_role = true
}

# Vault credential store in the project scope. Boundary's controller authenticates
# to Vault with the dedicated periodic token (NOT root) and self-renews it.
resource "boundary_credential_store_vault" "this" {
  name        = "vault"
  description = "Vault credential store for ${var.project_name} (dedicated least-privilege token)"
  scope_id    = boundary_scope.project.id

  address         = var.vault_cred_store_address
  token           = vault_token.boundary.client_token
  tls_skip_verify = true # base uses a self-signed cert
}

# One Vault SSH-certificate credential library per project, injected by every
# workspace target in this project. On each session Boundary generates an
# ephemeral ed25519 keypair, has Vault sign it against this project's signing role,
# and INJECTS the cert+key into the session — the developer holds no key. key_id is
# stamped with the authenticated developer's email (resolved per session) for audit.
resource "boundary_credential_library_vault_ssh_certificate" "this" {
  name                = "dev-workspace-ssh-cert"
  description         = "Signs workspace SSH certs for ${var.project_name}"
  credential_store_id = boundary_credential_store_vault.this.id
  path                = "${vault_mount.ssh.path}/sign/${vault_ssh_secret_backend_role.dev_workspace.name}"
  username            = var.workspace_user
  key_type            = "ed25519"
  key_id              = "{{.User.Email}}"
}
