# ---------------------------------------------------------------------------
# Identity module: IBM Verify OIDC SSO for Boundary and Nomad.
#
# This module never touches the frozen base instance. It creates two OIDC
# applications in the IBM Verify tenant via its REST API and wires Boundary +
# Nomad to trust them (with group-driven readonly/admin authorization).
#
# Base connection details (boundary_addr, nomad_addr) and the configured
# provider connections are supplied by the root module.
# ---------------------------------------------------------------------------

locals {
  tenant_url = "https://${var.ibm_verify_tenant}"
  # IBM Verify SaaS OIDC issuer; discovery at <issuer>/.well-known/openid-configuration
  issuer = "${local.tenant_url}/oidc/endpoint/default"

  boundary_addr = var.boundary_addr
  nomad_addr    = var.nomad_addr

  # OIDC redirect/callback URLs the relying parties register with the IdP.
  boundary_redirect_uri = "${local.boundary_addr}/v1/auth-methods/oidc:authenticate:callback"
  nomad_redirect_uris = [
    "${local.nomad_addr}/oidc/callback",
    "${local.nomad_addr}/ui/settings/tokens",
    "http://localhost:4649/oidc/callback", # Nomad CLI loopback (`nomad login`)
  ]
}
