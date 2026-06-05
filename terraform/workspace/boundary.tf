# ---------------------------------------------------------------------------
# Per-developer isolation — the security crux.
#
# Identity is the EXISTING IBM Verify OIDC login (no password accounts): the
# developer is matched by their /token/email claim via a managed group, mirroring
# the claim-filter pattern in the identity module. The managed group is a
# principal ONLY on the role for THIS workspace's target, so `boundary connect`
# to another developer's target is denied (the required negative test).
#
# The Vault SSH-certificate credential library is NOT created here — it is the
# project tier's, shared by every workspace in the project and referenced by id
# (local.p.credential_library_id). Isolation lives in the target/role/managed-group
# graph, not in the credential library.
# ---------------------------------------------------------------------------

# Static host catalog -> host -> host set -> ssh target. The co-located Boundary
# worker reaches the workspace SSH host port at the node's private IP (where
# Nomad's docker driver publishes the static port — NOT 127.0.0.1); nothing is
# added to the NLB or any security group.
resource "boundary_host_catalog_static" "ws" {
  name     = "ws-${local.slug}"
  scope_id = local.p.project_scope_id
}

resource "boundary_host_static" "ws" {
  name            = "ws-${local.slug}"
  host_catalog_id = boundary_host_catalog_static.ws.id
  address         = local.f.instance_private_ip
}

resource "boundary_host_set_static" "ws" {
  name            = "ws-${local.slug}"
  host_catalog_id = boundary_host_catalog_static.ws.id
  host_ids        = [boundary_host_static.ws.id]
}

# SSH target (NOT tcp): credential injection is only supported on ssh targets.
# Boundary's worker speaks SSH to the host:port and injects the Vault-signed cert.
# session_max_seconds governs connected-session length (the 5m cert TTL only
# covers the handshake); -1 connection limit allows VSCode's multiple connections.
resource "boundary_target" "ws" {
  name         = "ws-${local.slug}"
  description  = "SSH to ${var.developer_handle}'s workspace ${var.workspace_name} (project ${var.project_name})"
  type         = "ssh"
  scope_id     = local.p.project_scope_id
  default_port = var.ssh_port

  host_source_ids = [boundary_host_set_static.ws.id]

  injected_application_credential_source_ids = [local.p.credential_library_id]

  session_max_seconds      = var.workspace_session_max_seconds
  session_connection_limit = -1
}

# Managed group for this workspace, matched on the developer's IBM Verify email.
# Scoped per-workspace (dev-<handle>-<workspace>) rather than per-developer so each
# workspace's state is self-contained — a developer with two workspaces gets two
# groups (both matching their email), avoiding a cross-state shared resource.
resource "boundary_managed_group" "dev" {
  name           = "dev-${local.slug}"
  description    = "IBM Verify developer ${var.developer_handle} (${var.developer_email})"
  auth_method_id = local.f.boundary_oidc_auth_method_id
  filter         = "\"/token/email\" == \"${var.developer_email}\""
}

# ONLY this developer may authorize a session on this target.
resource "boundary_role" "dev_ws" {
  name            = "ws-${local.slug}"
  description     = "${var.developer_handle} -> authorize-session on their workspace target only"
  scope_id        = local.p.project_scope_id
  grant_scope_ids = ["this"]
  principal_ids   = [boundary_managed_group.dev.id]
  grant_strings   = ["ids=${boundary_target.ws.id};actions=authorize-session,read"]
}

# ---------------------------------------------------------------------------
# Target alias — the transparent-session front door. One global-scope alias for
# the target. The Boundary Client Agent on the laptop intercepts DNS for the
# alias and brokers the session, so the developer connects with a plain
# `ssh <alias>` (VSCode Remote-SSH to a normal Host) instead of
# `boundary connect ssh -target-id ...`. Credential injection is unchanged.
# value = <ws>.<dev>.<project>.<suffix>, unique by construction.
# authorize_session_host_id pins the single host on the target.
# ---------------------------------------------------------------------------
resource "boundary_alias_target" "ws" {
  scope_id                  = "global"
  name                      = "ws-${local.slug}"
  value                     = "${var.workspace_name}.${var.developer_handle}.${var.project_name}.${var.alias_suffix}"
  destination_id            = boundary_target.ws.id
  authorize_session_host_id = boundary_host_static.ws.id
}

# ---------------------------------------------------------------------------
# Alias-resolution grant. The Client Agent periodically lists the aliases a user
# can resolve to populate its local DNS-match cache; without this self-scoped
# `list-resolvable-aliases` action every laptop DNS lookup hits the controller.
# Self-scoped to {{.User.Id}} and returns ONLY aliases whose target the developer
# already has authorize-session on, so it does not widen access. Global scope
# because the `user` resource lives in the global scope.
# ---------------------------------------------------------------------------
resource "boundary_role" "dev_resolve_aliases" {
  name            = "dev-resolve-aliases-${local.slug}"
  description     = "Let ${var.developer_handle}'s Client Agent cache the aliases they can already reach"
  scope_id        = "global"
  grant_scope_ids = ["this"]
  principal_ids   = [boundary_managed_group.dev.id]
  grant_strings   = ["ids={{.User.Id}};type=user;actions=list-resolvable-aliases"]
}
