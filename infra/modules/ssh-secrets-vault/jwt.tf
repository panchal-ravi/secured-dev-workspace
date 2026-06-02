# Read-only policy: a Nomad workload may read ONLY the SSH CA public key. The CA
# public key is not a secret, but routing it through WIF establishes the
# federation path that later dynamic-credential phases reuse.
resource "vault_policy" "nomad_ca_read" {
  name = "nomad-workspace-ca-read"

  policy = <<-HCL
    path "ssh-client-signer/config/ca" {
      capabilities = ["read"]
    }
  HCL
}

# JWT auth method trusting Nomad's workload-identity signing keys (WIF). Vault
# fetches the JWKS from Nomad over HTTPS and validates it with the Nomad CA.
resource "vault_jwt_auth_backend" "nomad" {
  path         = "jwt-nomad"
  type         = "jwt"
  description  = "Nomad workload-identity federation"
  jwks_url     = var.nomad_jwks_url
  jwks_ca_pem  = var.nomad_ca_pem
  default_role = "dev-workspace"
}

# Role: any Nomad workload presenting a JWT with aud=vault.io gets a short,
# read-only token. user_claim uses the per-job id (JSON-pointer form).
# NOTE: bound_audiences MUST match the Nomad agent default_identity.aud
# (config/nomad.hcl) and the job identity.aud — a mismatch fails JWT verification
# with a silent 403. All three are intentionally the fixed literal "vault.io".
resource "vault_jwt_auth_backend_role" "dev_workspace" {
  backend                 = vault_jwt_auth_backend.nomad.path
  role_name               = "dev-workspace"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.nomad_ca_read.name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
}
