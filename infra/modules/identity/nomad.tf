# ---------------------------------------------------------------------------
# Nomad: OIDC auth method + group-driven ACL policies and binding rules.
#
# Binding rules attach ACL *policies* to the SSO-issued token. "admin" here is
# therefore a maximally-privileged policy, NOT a true management token (Nomad
# cannot grant management via binding rules) — the bootstrap management token
# remains the break-glass super-user.
# ---------------------------------------------------------------------------

resource "nomad_acl_auth_method" "ibm_verify" {
  name           = "ibm-verify"
  type           = "OIDC"
  token_locality = "global"
  max_token_ttl  = "1h"
  default        = false

  config {
    oidc_discovery_url = local.issuer
    oidc_client_id     = local.nomad_client_id
    oidc_client_secret = local.nomad_client_secret

    bound_audiences       = [local.nomad_client_id]
    oidc_scopes           = ["openid", "email", "groups"]
    allowed_redirect_uris = local.nomad_redirect_uris

    # Expose the multi-valued `groups` claim to binding-rule selectors as
    # list.groups.
    list_claim_mappings = {
      "groups" = "groups"
    }
  }
}

resource "nomad_acl_policy" "readonly" {
  name        = "readonly"
  description = "Read-only across all namespaces (IBM Verify readonly group)"

  rules_hcl = <<-EOT
    namespace "*" {
      policy = "read"
    }
    node {
      policy = "read"
    }
    agent {
      policy = "read"
    }
    operator {
      policy = "read"
    }
  EOT
}

resource "nomad_acl_policy" "admin" {
  name        = "admin"
  description = "Full write across all namespaces (IBM Verify admins group)"

  rules_hcl = <<-EOT
    namespace "*" {
      policy = "write"
    }
    node {
      policy = "write"
    }
    agent {
      policy = "write"
    }
    operator {
      policy = "write"
    }
    plugin {
      policy = "write"
    }
  EOT
}

resource "nomad_acl_binding_rule" "admins" {
  auth_method = nomad_acl_auth_method.ibm_verify.name
  description = "Members of ${var.admin_group_name} -> admin policy"
  selector    = "\"${var.admin_group_name}\" in list.groups"
  bind_type   = "policy"
  bind_name   = nomad_acl_policy.admin.name
}

resource "nomad_acl_binding_rule" "readonly" {
  auth_method = nomad_acl_auth_method.ibm_verify.name
  description = "Members of ${var.readonly_group_name} -> readonly policy"
  selector    = "\"${var.readonly_group_name}\" in list.groups"
  bind_type   = "policy"
  bind_name   = nomad_acl_policy.readonly.name
}

# Defense-in-depth ONLY (dev-workspace phase). In the target design developers
# never receive a Nomad token — the portal is their interface and Boundary the
# authorization boundary — so this namespace-scoped policy is belt-and-suspenders
# for the case where a developer IS ever handed Nomad SSO access: it confines them
# to writing within their project namespace. One example namespace; not a generator.
resource "nomad_acl_policy" "project_dev" {
  name        = "project-${var.workspace_namespace}-dev"
  description = "Write within the ${var.workspace_namespace} namespace only (IBM Verify developers group)"

  rules_hcl = <<-EOT
    namespace "${var.workspace_namespace}" {
      policy = "write"
    }
  EOT
}

resource "nomad_acl_binding_rule" "project_dev" {
  auth_method = nomad_acl_auth_method.ibm_verify.name
  description = "Members of ${var.developers_group_name} -> ${var.workspace_namespace} namespace only"
  selector    = "\"${var.developers_group_name}\" in list.groups"
  bind_type   = "policy"
  bind_name   = nomad_acl_policy.project_dev.name
}
