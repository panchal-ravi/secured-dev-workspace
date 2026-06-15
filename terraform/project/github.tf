# ---------------------------------------------------------------------------
# Per-project GitHub App credential broker. Enables the registered GitHub
# secrets plugin (foundation tier baked + registered it) at github/<project>,
# configures it with THIS project's GitHub App, and defines a pre-scoped
# permission set. The workspace's Nomad task mints a short-lived (1h,
# non-renewable) GitHub App installation token from the permission-set token
# path over WIF — that token is the git push credential. No static PAT anywhere;
# the App private key lives only in Vault.
#
# Manual prerequisite (operator, per project): create a GitHub App with
# repository permission Contents: Read & write (+ Metadata: read), install it on
# the project's repos, and supply app_id / installation_id / private key (PKCS#1
# PEM) via the gitignored <project>.tfvars.
# ---------------------------------------------------------------------------

locals {
  # The pre-scoped permission set + its constrained token path. Named to mirror
  # the SSH signing role. The workspace reads github/<project>/token/<name>.
  github_permissionset_name = "dev-workspace"
}

# Per-project mount of the external plugin (type = the registered plugin name).
resource "vault_mount" "github" {
  namespace   = vault_namespace.project.path
  path        = "github/${var.project_name}"
  type        = "vault-plugin-secrets-github"
  description = "GitHub App token broker for project ${var.project_name}"
}

# Configure the mount with the project's GitHub App. prv_key is the App private
# key (PKCS#1 PEM). disable_read: the config holds the private key — never read back.
resource "vault_generic_endpoint" "github_config" {
  namespace            = vault_namespace.project.path
  path                 = "${vault_mount.github.path}/config"
  ignore_absent_fields = true
  disable_read         = true

  data_json = jsonencode({
    app_id  = var.github_app_id
    prv_key = var.github_app_private_key
  })
}

# Pre-scoped permission set: binds the installation id and the minimal
# permission (contents:write for git push; GitHub auto-adds metadata:read). If
# github_repositories is set, the token is further constrained to those repos;
# otherwise it covers all repos the App installation can access. The workspace
# reads a token from this set with NO parameters, so installation_id and the
# permission scope never leave Vault.
resource "vault_generic_endpoint" "github_permissionset" {
  namespace            = vault_namespace.project.path
  path                 = "${vault_mount.github.path}/permissionset/${local.github_permissionset_name}"
  ignore_absent_fields = true
  disable_read         = true

  data_json = jsonencode(merge(
    {
      installation_id = var.github_app_installation_id
      permissions     = { contents = "write" }
    },
    length(var.github_repositories) > 0 ? { repositories = var.github_repositories } : {}
  ))

  depends_on = [vault_generic_endpoint.github_config]
}

# WIF grant: the workspace task (via its per-project WIF role) may READ a token
# from the pre-scoped permission-set token path only. Added to the role's
# token_policies in vault.tf alongside the SSH-CA-read policy.
resource "vault_policy" "nomad_github_token" {
  namespace = vault_namespace.project.path
  name      = "nomad-${var.project_name}-github-token"

  policy = <<-HCL
    path "${vault_mount.github.path}/token/${local.github_permissionset_name}" {
      capabilities = ["read"]
    }
  HCL
}
