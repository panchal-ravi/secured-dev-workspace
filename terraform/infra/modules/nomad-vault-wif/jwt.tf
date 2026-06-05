# Foundation-level Nomad↔Vault workload-identity federation (WIF): the single
# jwt-nomad auth method that trusts Nomad's workload-identity signing keys. Vault
# fetches the JWKS from Nomad over HTTPS and validates it with the Nomad CA.
#
# This is the platform-tier half of WIF — created once. The per-project READ role
# + policy (which CA path a project's workspace task may read) live in
# modules/project, so a project's roles cannot read another project's SSH CA.
# There is intentionally NO default_role here: every workspace job names its
# project's role explicitly via the task `vault { role = "<project>" }` block.
resource "vault_jwt_auth_backend" "nomad" {
  path        = "jwt-nomad"
  type        = "jwt"
  description = "Nomad workload-identity federation"
  jwks_url    = var.nomad_jwks_url
  jwks_ca_pem = var.nomad_ca_pem
}
