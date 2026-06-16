# Portal blueprint-provisioning policy (B2 / R3 — project-MCP deploy plane).
#
# Attached to the portal's ROOT-namespace WIF identity
# (vault_jwt_auth_backend_role.infra_portal, developer-portal.tf), gated on the
# onboarding plane. At runtime the portal's blueprint Executor scopes EVERY call to
# a project namespace via client.WithNamespace(<project>), so these RELATIVE paths
# apply INSIDE the targeted project namespace. The grant is the minimum needed to
# instantiate / deprovision a credential blueprint:
#   - mount / unmount the instance's secret engines        (sys/mounts/*)
#   - write / delete the generated least-privilege policies (sys/policies/acl/*, lint-gated)
#   - manage the JWT/WIF roles MCP jobs assume              (auth/jwt-nomad/role/*)
#   - configure DB connections + roles, rotate root         (database/*)
#   - write class-B static secrets                          (secret/* — the project KV mount)
#   - revoke dynamic leases on deprovision                  (sys/leases/revoke-prefix/*)
#
# CONTAINMENT — read honestly:
# Because the policy is attached in the ROOT namespace and its paths are relative, it
# is usable in ANY namespace the portal targets; it is NOT physically pinned to one
# project by Vault attachment scope. The boundary is therefore TWO application-level
# controls plus the deny stanzas below — not namespace attachment:
#   1. the Executor ALWAYS sends a project-namespace header — DeployServer rejects an
#      empty descriptor.Namespace with ErrBadRequest — so it never operates in root;
#   2. the Executor lints every generated ACL policy before writing it (a generated
#      policy may target only the project namespace's own mounts);
#   3. the explicit deny rules below forbid the sensitive surfaces in the standing
#      constraint (identity, auth-method admin, namespace lifecycle, cubbyhole) and
#      win over any allow above.
# A DEDICATED per-project provisioner identity (physical per-namespace pinning) is the
# documented future hardening (spec §5). It requires portal -> child-namespace token
# brokering that does not exist today; deferred.

# --- allow: blueprint instantiation surface (relative to the targeted namespace) ---
path "sys/mounts" {
  capabilities = ["read"]
}

path "sys/mounts/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "sys/policies/acl" {
  capabilities = ["list"]
}

path "sys/policies/acl/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "auth/jwt-nomad/role/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "database/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "secret/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "sys/leases/revoke-prefix/*" {
  capabilities = ["update"]
}

# --- deny: standing constraint surfaces (deny wins over allow) ---
path "identity/*" {
  capabilities = ["deny"]
}

path "sys/auth" {
  capabilities = ["deny"]
}

path "sys/auth/*" {
  capabilities = ["deny"]
}

path "sys/namespaces" {
  capabilities = ["deny"]
}

path "sys/namespaces/*" {
  capabilities = ["deny"]
}

path "cubbyhole/*" {
  capabilities = ["deny"]
}

# The portal must never rewrite its own provisioning policy (privilege-escalation guard).
path "sys/policies/acl/portal-blueprint-provisioning" {
  capabilities = ["deny"]
}
