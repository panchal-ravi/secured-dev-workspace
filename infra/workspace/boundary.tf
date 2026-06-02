# ---------------------------------------------------------------------------
# Per-developer isolation — the security crux.
#
# Identity is the EXISTING IBM Verify OIDC login (no password accounts): each
# developer is matched by their /token/email claim via a managed group, mirroring
# the claim-filter pattern in infra/modules/identity/boundary.tf. A developer's
# managed group is a principal ONLY on the role(s) for THEIR target(s), so
# `boundary connect` to another developer's target is denied (the required
# negative test).
# ---------------------------------------------------------------------------

# One static host catalog -> host -> host set -> ssh target per workspace. The
# co-located Boundary worker reaches the workspace's SSH host port at the node's
# private IP (where Nomad's docker driver publishes the static port — NOT 127.0.0.1);
# nothing is added to the NLB or any security group.
resource "boundary_host_catalog_static" "ws" {
  for_each = local.workspace_units
  name     = "ws-${local.unit_slug[each.key]}"
  scope_id = var.boundary_project_scope_id
}

resource "boundary_host_static" "ws" {
  for_each        = local.workspace_units
  name            = "ws-${local.unit_slug[each.key]}"
  host_catalog_id = boundary_host_catalog_static.ws[each.key].id
  address         = var.workspace_host_address
}

resource "boundary_host_set_static" "ws" {
  for_each        = local.workspace_units
  name            = "ws-${local.unit_slug[each.key]}"
  host_catalog_id = boundary_host_catalog_static.ws[each.key].id
  host_ids        = [boundary_host_static.ws[each.key].id]
}

# SSH target (NOT tcp): credential injection is only supported on ssh targets.
# Boundary's worker speaks SSH to the host:port and injects the Vault-signed cert.
# session_max_seconds governs connected-session length (the 5m cert TTL only
# covers the handshake); -1 connection limit allows VSCode's multiple connections.
resource "boundary_target" "ws" {
  for_each     = local.workspace_units
  name         = "ws-${local.unit_slug[each.key]}"
  description  = "SSH to ${each.value.dev_name}'s workspace ${each.value.ws_name} (project ${each.value.project})"
  type         = "ssh"
  scope_id     = var.boundary_project_scope_id
  default_port = each.value.ssh_port

  host_source_ids = [boundary_host_set_static.ws[each.key].id]

  injected_application_credential_source_ids = [
    boundary_credential_library_vault_ssh_certificate.ws.id
  ]

  session_max_seconds      = var.workspace_session_max_seconds
  session_connection_limit = -1
}

# One managed group per developer, matched on their IBM Verify email claim.
resource "boundary_managed_group" "dev" {
  for_each       = var.developers
  name           = "dev-${each.key}"
  description    = "IBM Verify developer ${each.key} (${each.value.email})"
  auth_method_id = var.boundary_oidc_auth_method_id
  filter         = "\"/token/email\" == \"${each.value.email}\""
}

# Per-workspace role: ONLY this developer may authorize a session on this target.
resource "boundary_role" "dev_ws" {
  for_each        = local.workspace_units
  name            = "ws-${local.unit_slug[each.key]}"
  description     = "${each.value.dev_name} -> authorize-session on their workspace target only"
  scope_id        = var.boundary_project_scope_id
  grant_scope_ids = ["this"]
  principal_ids   = [boundary_managed_group.dev[each.value.dev_name].id]
  grant_strings   = ["ids=${boundary_target.ws[each.key].id};actions=authorize-session,read"]
}

# One shared Vault SSH-certificate credential library on the existing Vault
# credential store, injected by every workspace target — the per-developer
# isolation lives in the target/role/managed-group graph, not here. All targets
# sign through the same role with identical params; a per-target library would
# only matter if those params diverged. On each session Boundary generates an
# ephemeral ed25519 keypair, has Vault sign it (path = the dev-workspace signing
# role), and INJECTS the cert+key into the session — the developer holds no key.
# key_id is stamped with the authenticated developer's email (resolved per
# session) for audit attribution.
resource "boundary_credential_library_vault_ssh_certificate" "ws" {
  name                = "dev-workspace-ssh-cert"
  credential_store_id = var.vault_credential_store_id
  path                = var.vault_ssh_sign_path
  username            = "dev"
  key_type            = "ed25519"
  key_id              = "{{.User.Email}}"
}

# ---------------------------------------------------------------------------
# Target aliases — the transparent-session front door. One global-scope alias
# per workspace target. The Boundary Client Agent on the developer's laptop
# intercepts DNS for the alias value and brokers the session, so the developer
# connects with a plain `ssh <alias>` (hence VSCode Remote-SSH to a normal Host)
# instead of `boundary connect ssh -target-id ...`. Credential injection is
# unchanged — the worker still injects the Vault-signed cert. Aliases are only
# supported at the global scope; value = <ws>.<dev>.<project>.<suffix>, unique
# by construction. authorize_session_host_id pins the single host on the target.
# ---------------------------------------------------------------------------
resource "boundary_alias_target" "ws" {
  for_each                  = local.workspace_units
  scope_id                  = "global"
  name                      = "ws-${local.unit_slug[each.key]}"
  value                     = "${each.value.ws_name}.${each.value.dev_name}.${each.value.project}.${var.alias_suffix}"
  destination_id            = boundary_target.ws[each.key].id
  authorize_session_host_id = boundary_host_static.ws[each.key].id
}

# ---------------------------------------------------------------------------
# Alias-resolution grant. The Client Agent periodically lists the aliases a user
# can resolve to populate its local DNS-match cache; without this self-scoped
# `list-resolvable-aliases` action every laptop DNS lookup hits the controller.
# It is self-scoped to {{.User.Id}} and returns ONLY aliases whose target the
# developer already has authorize-session on, so it does not widen access —
# isolation still lives entirely in the target/role/managed-group graph. Created
# explicitly (not relying on the default role) to keep the requirement legible;
# additive and harmless if the default authenticated-user role already has it.
# Global scope because the `user` resource lives in the global scope.
# ---------------------------------------------------------------------------
resource "boundary_role" "dev_resolve_aliases" {
  name            = "dev-resolve-aliases"
  description     = "Let developers' Client Agent cache the aliases they can already reach"
  scope_id        = "global"
  grant_scope_ids = ["this"]
  principal_ids   = [for d in keys(var.developers) : boundary_managed_group.dev[d].id]
  grant_strings   = ["ids={{.User.Id}};type=user;actions=list-resolvable-aliases"]
}
