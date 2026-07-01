# Portal blueprint-provisioning policy (B2 / R3 — project-MCP deploy plane).
#
# Attached to the portal's ROOT-namespace WIF identity
# (vault_jwt_auth_backend_role.infra_portal, developer-portal.tf), gated on the
# onboarding plane. At runtime the portal's blueprint Executor scopes EVERY call to
# a project namespace via client.WithNamespace(<project>).
#
# NAMESPACE PREFIX — why every path starts with `+/` (and an UNRESOLVED caveat):
# This policy is attached in the ROOT namespace, but it is ONLY ever used against a
# CHILD namespace (the Executor refuses to operate in root). Vault evaluates a
# root-token request to a child namespace against the NAMESPACE-PREFIXED path
# (`<child>/sys/policies/acl/…`), NOT the bare relative path — so a relative grant
# like `sys/policies/acl/*` matches nothing in a child and every write 403s.
#
# The PROJECT-deploy plane targets DYNAMIC namespaces (one per project, unknowable to
# this static infra-tier policy), so it cannot use explicit per-namespace prefixes;
# the paths below are prefixed with `+/` (Vault's single-segment wildcard).
#
# KNOWN LIMITATION (confirmed, blocks real deploys): Vault resolves the namespace BEFORE
# ACL path-matching, so `+` does NOT match the namespace segment. A root-namespace token
# operating in a child namespace is matched against the namespace-PREFIXED path
# (`<child>/sys/policies/acl/…`), which neither a relative grant nor `+/` covers — only
# an EXPLICIT child prefix (e.g. `acme/sys/policies/acl/*`) matches, and that cannot be
# written for unknowable dynamic project namespaces. This was proven while debugging the
# blueprint flow against live Vault Enterprise. Real project deploys therefore need the
# spec §5 hardening BEFORE instantiate/deprovision will succeed in a project namespace:
# per-project policy attachment, or a dedicated per-namespace provisioner token brokered
# to the portal. Until then this policy documents the intended instantiation surface:
#   - mount / unmount the instance's secret engines        (+/sys/mounts/*)
#   - write / delete the generated least-privilege policies (+/sys/policies/acl/*, lint-gated)
#   - manage the JWT/WIF roles MCP jobs assume              (+/auth/jwt-nomad/role/*)
#   - configure DB connections + roles, rotate root         (+/database/*)
#   - write class-B static secrets                          (+/secret/* — the project KV mount)
#   - revoke dynamic leases on deprovision                  (+/sys/leases/revoke-prefix/*)
#
# CONTAINMENT — read honestly:
# `+/` makes the grant usable in ANY first-level namespace the portal targets; it is
# NOT physically pinned to one project by Vault attachment scope. The boundary is
# therefore TWO application-level controls plus the deny stanzas below — not namespace
# attachment:
#   1. the Executor ALWAYS sends a project-namespace header — DeployServer rejects an
#      empty descriptor.Namespace with ErrBadRequest — so it never operates in root;
#   2. the Executor lints every generated ACL policy before writing it (a generated
#      policy may target only the project namespace's own mounts);
#   3. the explicit deny rules below forbid the sensitive surfaces in the standing
#      constraint (identity, auth-method admin, namespace lifecycle, cubbyhole) in
#      every namespace, and deny wins over any allow above.
# A DEDICATED per-project provisioner identity (physical per-namespace pinning) is the
# documented future hardening (spec §5). It requires portal -> child-namespace token
# brokering that does not exist today; deferred.

# --- allow: blueprint instantiation surface (one child namespace level via `+/`) ---
path "+/sys/mounts" {
  capabilities = ["read"]
}

path "+/sys/mounts/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "+/sys/policies/acl" {
  capabilities = ["list"]
}

path "+/sys/policies/acl/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "+/auth/jwt-nomad/role/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "+/database/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "+/secret/*" {
  capabilities = ["create", "read", "update", "delete"]
}

path "+/sys/leases/revoke-prefix/*" {
  capabilities = ["update"]
}

# --- deny: standing constraint surfaces (deny wins over allow) ---
# Denied both relative (root) and namespace-prefixed (`+/`) so the sensitive surfaces
# are forbidden whichever way a path could resolve.
path "identity/*" {
  capabilities = ["deny"]
}

path "+/identity/*" {
  capabilities = ["deny"]
}

path "sys/auth" {
  capabilities = ["deny"]
}

path "sys/auth/*" {
  capabilities = ["deny"]
}

path "+/sys/auth" {
  capabilities = ["deny"]
}

path "+/sys/auth/*" {
  capabilities = ["deny"]
}

path "sys/namespaces" {
  capabilities = ["deny"]
}

path "sys/namespaces/*" {
  capabilities = ["deny"]
}

path "+/sys/namespaces" {
  capabilities = ["deny"]
}

path "+/sys/namespaces/*" {
  capabilities = ["deny"]
}

path "cubbyhole/*" {
  capabilities = ["deny"]
}

path "+/cubbyhole/*" {
  capabilities = ["deny"]
}

# The portal must never rewrite its own provisioning policy (privilege-escalation
# guard) — in root (where it lives) or in any child namespace.
path "sys/policies/acl/portal-blueprint-provisioning" {
  capabilities = ["deny"]
}

path "+/sys/policies/acl/portal-blueprint-provisioning" {
  capabilities = ["deny"]
}
