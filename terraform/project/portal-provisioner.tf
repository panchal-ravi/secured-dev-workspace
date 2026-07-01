# ---------------------------------------------------------------------------
# Per-project provisioner brokering (§5). The Developer Portal does NOT hold a
# standing token that can write into this namespace. Instead it presents its
# second Nomad workload identity (aud=vault-provisioner) at this namespace's
# jwt-nomad backend and receives a short-TTL token NATIVE to the namespace. That
# token evaluates the RELATIVE-path policy below — so the namespace-prefix ACL
# match that broke the old root-token `+/` approach never arises.
#
# Blast radius = one project: the role only issues tokens for THIS namespace and
# is bound to the portal job identity in the infra Nomad namespace.
# ---------------------------------------------------------------------------

resource "vault_policy" "portal_provisioner" {
  namespace = vault_namespace.project.path
  name      = "portal-provisioner"

  # Relative paths: evaluated by a token native to this namespace, so no `+/` and
  # no namespace prefix. Grants exactly the blueprint engine's engine-lifecycle
  # surface; denies win over any grant.
  policy = <<-HCL
    # Secret-engine mount lifecycle (Class A database engine).
    path "sys/mounts" { capabilities = ["read"] }
    path "sys/mounts/*" { capabilities = ["create", "read", "update", "delete"] }

    # Generated least-privilege ACL policies.
    path "sys/policies/acl" { capabilities = ["list"] }
    path "sys/policies/acl/*" { capabilities = ["create", "read", "update", "delete", "list"] }

    # The MCP/workspace job's Nomad-WIF role.
    path "auth/jwt-nomad/role/*" { capabilities = ["create", "read", "update", "delete"] }

    # Class A database engine: connection/config, rotate-root, roles.
    path "database/*" { capabilities = ["create", "read", "update", "delete"] }

    # Class B write-only KV seed + project runtime coordinates.
    path "secret/*" { capabilities = ["create", "read", "update", "delete"] }

    # Lease-safe deprovision (matches RevokeLeasesByPrefix -> sys/leases/revoke-force).
    path "sys/leases/revoke-force/*" { capabilities = ["update"] }

    # --- Denies (win over grants) ---
    path "identity/*" { capabilities = ["deny"] }
    path "sys/auth" { capabilities = ["deny"] }
    path "sys/auth/*" { capabilities = ["deny"] }
    path "sys/namespaces" { capabilities = ["deny"] }
    path "sys/namespaces/*" { capabilities = ["deny"] }
    path "cubbyhole/*" { capabilities = ["deny"] }
    # No self-rewrite of the provisioner policy.
    path "sys/policies/acl/portal-provisioner" { capabilities = ["deny"] }
  HCL
}

# The portal exchanges its aud=vault-provisioner workload identity for a token
# under the policy above. bound_claims pins the caller to the portal job in the
# infra Nomad namespace, so no other workload can assume this role.
resource "vault_jwt_auth_backend_role" "portal_provisioner" {
  namespace = vault_namespace.project.path
  backend   = vault_jwt_auth_backend.nomad.path
  role_name = "portal-provisioner"
  role_type = "jwt"

  bound_audiences         = ["vault-provisioner"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  bound_claims = {
    nomad_namespace = "infra"
    nomad_job_id    = "developer-portal"
  }

  token_policies = [vault_policy.portal_provisioner.name]
  token_ttl      = 300
  token_type     = "service"
}
