# ---------------------------------------------------------------------------
# EXAMPLE PKI secrets engine — operator PRECONDITION for deploy-time path grants.
#
# The portal's deploy-time "additional Vault path grants" feature only GRANTS an
# MCP server's WIF token access to an engine that already exists in the project's
# Vault namespace; it never mounts engines itself (engine setup — CA, roles — is
# inherently engine-specific and belongs in this tier, alongside database.tf /
# vault.tf / github.tf). This file is a concrete, applyable example of that
# precondition. Any secrets engine works the same way; PKI is just illustrative.
#
# Gated OFF by default (var.enable_pki_example). Flip it on for the project you
# want to demo against, `terraform apply`, then a project-admin can deploy e.g.
# the vault-mcp server with an extra grant of `pki/issue/server` (capabilities
# create, update) and issue certs. The MCP token stays confined to this namespace;
# the grant is linted server-side (no sys/auth/identity/cubbyhole, no sudo/root).
# See terraform/project/README.md and the platform-admin runbook.
# ---------------------------------------------------------------------------

resource "vault_mount" "pki" {
  count = var.enable_pki_example ? 1 : 0

  namespace                 = vault_namespace.project.path
  path                      = "pki"
  type                      = "pki"
  description               = "Example PKI engine for project ${var.project_name} (path-grant demo)"
  default_lease_ttl_seconds = 3600  # 1h
  max_lease_ttl_seconds     = 86400 # 24h
}

# Self-signed root CA generated inside the engine (demo only — a real deployment
# would issue an intermediate signed by an external/offline root).
resource "vault_pki_secret_backend_root_cert" "root" {
  count = var.enable_pki_example ? 1 : 0

  namespace   = vault_namespace.project.path
  backend     = vault_mount.pki[0].path
  type        = "internal"
  common_name = "${var.project_name} demo root CA"
  ttl         = 86400
}

# A role the granted MCP server issues leaf certs from: `pki/<project>/issue/server`.
resource "vault_pki_secret_backend_role" "server" {
  count = var.enable_pki_example ? 1 : 0

  namespace        = vault_namespace.project.path
  backend          = vault_mount.pki[0].path
  name             = "server"
  allowed_domains  = ["${var.project_name}.svc", "localhost"]
  allow_subdomains = true
  allow_localhost  = true
  max_ttl          = 3600
  key_type         = "ed25519"

  depends_on = [vault_pki_secret_backend_root_cert.root]
}
