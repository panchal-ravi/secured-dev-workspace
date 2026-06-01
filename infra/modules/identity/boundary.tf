# ---------------------------------------------------------------------------
# Boundary: trust IBM Verify as an OIDC IdP and map groups -> roles.
#
# The OIDC method is set PRIMARY for its scope. Boundary only auto-creates a
# user on first OIDC login when the method is primary; otherwise the callback
# authenticates the account but dead-ends at "user not found ... refusing to
# auto-create user". Primary governs only auto-vivification of NEW users, not
# authentication of EXISTING ones, so the break-glass recovery-KMS / password
# admin (that user already exists) keeps working as before.
# ---------------------------------------------------------------------------

resource "boundary_auth_method_oidc" "ibm_verify" {
  name        = "ibm-verify"
  description = "IBM Verify SSO (OIDC)"
  scope_id    = "global"

  issuer        = local.issuer
  client_id     = local.boundary_client_id
  client_secret = local.boundary_client_secret

  signing_algorithms = ["RS256"]
  api_url_prefix     = local.boundary_addr
  claims_scopes      = ["email", "groups"]

  is_primary_for_scope = true
  state                = "active-public"
}

# Managed groups auto-evaluate the OIDC claims for each authenticated account.
# Boundary exposes token claims under /token/<claim>.
resource "boundary_managed_group" "admins" {
  name           = "ibm-verify-admins"
  description    = "IBM Verify members of ${var.admin_group_name}"
  auth_method_id = boundary_auth_method_oidc.ibm_verify.id
  filter         = "\"${var.admin_group_name}\" in \"/token/groups\""
}

resource "boundary_managed_group" "readonly" {
  name           = "ibm-verify-readonly"
  description    = "IBM Verify members of ${var.readonly_group_name}"
  auth_method_id = boundary_auth_method_oidc.ibm_verify.id
  filter         = "\"${var.readonly_group_name}\" in \"/token/groups\""
}

# Admin: full grants across global + all descendant scopes (org, project).
resource "boundary_role" "admins" {
  name            = "ibm-verify-admins"
  description     = "Full administrator (IBM Verify admins group)"
  scope_id        = "global"
  grant_scope_ids = ["this", "descendants"]
  grant_strings   = ["ids=*;type=*;actions=*"]
  principal_ids   = [boundary_managed_group.admins.id]
}

# Read-only: list/read everything across global + descendants, no mutations.
resource "boundary_role" "readonly" {
  name            = "ibm-verify-readonly"
  description     = "Read-only access (IBM Verify readonly group)"
  scope_id        = "global"
  grant_scope_ids = ["this", "descendants"]
  grant_strings   = ["ids=*;type=*;actions=read,list"]
  principal_ids   = [boundary_managed_group.readonly.id]
}
