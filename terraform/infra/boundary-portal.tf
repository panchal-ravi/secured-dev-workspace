# ---------------------------------------------------------------------------
# Project-create plane — Boundary (decision F-A: dedicated scoped account).
#
# The portal authenticates to Boundary as this dedicated password account instead
# of the global bootstrap admin. It is granted the union of what the portal
# actually does — and NOTHING at the global scope beyond that:
#
#   1. portal_org_admin  — admin WITHIN the org scope and its descendant project
#      scopes: create/manage project scopes (the new project-create plane) and the
#      per-workspace graph (host catalogs, targets, cred stores/libraries, roles,
#      aliases, sessions). It canNOT touch the global scope.
#   2. portal_global_mgroups — a bounded global grant covering the THREE global-scope
#      writes the workspace-launch path (boundary.go:Provision) performs:
#        - OIDC managed groups on the global ibm_verify auth method (per-developer
#          identity binding),
#        - a per-developer global role `resolve-aliases-<handle>` (grants the dev
#          list-resolvable-aliases on self),
#        - a per-workspace global target alias.
#      It does NOT grant global users, other-org admin, or auth-method write.
#
# Net tightening vs. the bootstrap admin: no global user administration, no
# auth-method write, no reach outside this org's subtree.
#
# DECISION (2026-07-01): Option A (broaden the global grant) chosen for simplicity.
# RESIDUAL PRIVILEGE ACCEPTED: type=role;create + add-grants at global is a
# self-escalation path (the portal could mint a global role granting itself more).
# `delete` on roles is deliberately withheld so it cannot remove Boundary's own
# admin roles.
#
# HARDENING TODO — Option B (remove global role/alias writes; tracked in memory
# [[portal-driven-project-lifecycle]] and the plan): replace the per-developer
# `resolve-aliases-<handle>` role with ONE static global role for `u_auth`
# (created here at apply time), and drop portal-created target aliases (the shipped
# connect path uses `boundary connect -target-id`, not the alias). Then this grant
# shrinks back to managed-group + auth-method-read only, closing the escalation path.
# ---------------------------------------------------------------------------

resource "random_password" "portal_boundary" {
  count   = local.project_creator_count
  length  = 32
  special = false
}

resource "boundary_account_password" "portal" {
  count          = local.project_creator_count
  auth_method_id = module.secured_codespace.admin_auth_method_id
  login_name     = "developer-portal"
  password       = random_password.portal_boundary[0].result
}

resource "boundary_user" "portal" {
  count       = local.project_creator_count
  name        = "developer-portal"
  description = "Developer Portal service identity (project-create + workspace provisioning)"
  scope_id    = "global"
  account_ids = [boundary_account_password.portal[0].id]
}

# Admin within the org scope + every descendant project scope (project scopes,
# per-workspace graph). Not global.
resource "boundary_role" "portal_org_admin" {
  count           = local.project_creator_count
  name            = "portal-org-admin"
  description     = "Portal: manage project scopes + the per-workspace graph within the org (not global)"
  scope_id        = module.secured_codespace.org_scope_id
  grant_scope_ids = ["this", "children"]
  grant_strings   = ["ids=*;type=*;actions=*"]
  principal_ids   = [boundary_user.portal[0].id]
}

# Bounded global grant: the three global-scope writes workspace provisioning makes —
# OIDC managed groups, the per-developer resolve-aliases role, and target aliases.
# See the header for the Option A/B decision. `type=role` withholds `delete` so the
# portal cannot remove Boundary's own admin roles.
resource "boundary_role" "portal_global_mgroups" {
  count           = local.project_creator_count
  name            = "portal-global-managed-groups"
  description     = "Portal: global writes needed by workspace provisioning (managed groups, resolve-aliases role, target aliases)"
  scope_id        = "global"
  grant_scope_ids = ["this"]
  grant_strings = [
    "ids=*;type=managed-group;actions=create,read,update,delete,list",
    "ids=*;type=auth-method;actions=read,list",
    "ids=*;type=role;actions=create,read,update,list,add-grants,remove-grants,set-grants,add-principals,set-grant-scopes",
    "ids=*;type=alias;actions=create,read,update,delete,list",
  ]
  principal_ids = [boundary_user.portal[0].id]
}
