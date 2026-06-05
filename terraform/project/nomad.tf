# ---------------------------------------------------------------------------
# Nomad namespace = project, plus a defense-in-depth namespace-scoped ACL policy
# + binding rule (moved here from the foundation identity module so it is
# per-project). Confines a developer — IF ever handed Nomad SSO — to writing
# within THEIR project namespace only. Developers normally reach workspaces
# through Boundary, not Nomad, so this is belt-and-suspenders.
# ---------------------------------------------------------------------------

resource "nomad_namespace" "project" {
  name        = var.project_name
  description = "Project namespace for ${var.project_name}"
}

resource "nomad_acl_policy" "project_dev" {
  name        = "project-${var.project_name}-dev"
  description = "Write within the ${var.project_name} namespace only (IBM Verify ${var.developers_group_name} group)"

  rules_hcl = <<-EOT
    namespace "${var.project_name}" {
      policy = "write"
    }
  EOT
}

resource "nomad_acl_binding_rule" "project_dev" {
  auth_method = local.f.nomad_oidc_auth_method_name
  description = "Members of ${var.developers_group_name} -> ${var.project_name} namespace only"
  selector    = "\"${var.developers_group_name}\" in list.groups"
  bind_type   = "policy"
  bind_name   = nomad_acl_policy.project_dev.name
}
