# ---------------------------------------------------------------------------
# Per-project Vault Enterprise NAMESPACE. Replaces path-based isolation: every
# project-owned secret engine, policy, token and WIF role now lives INSIDE this
# namespace (set via the per-resource `namespace` argument), so a project token
# is physically incapable of addressing another project, the root namespace, or
# sys/ outside it. The namespace is created first; child resources reference
# vault_namespace.project.path.
#
# COLLISION GUARD: a project name is globally unique (it is simultaneously the
# Vault namespace, the Nomad namespace, and the Boundary scope). We cannot gate
# creation with a `data "vault_namespace"` source — it errors when the namespace
# is ABSENT, which is the normal new-project case. So the guard is a documented
# operator pre-check, run before `apply`:
#
#   VAULT_SKIP_VERIFY=true vault namespace list 2>/dev/null | grep -qx "<project>/" \
#     && echo "COLLISION: namespace <project> exists — choose another name" && exit 1
#
# Vault also hard-fails the apply if vault_namespace.project collides (400
# "namespace already exists"), so creation is never silently idempotent over an
# existing tenant. The friendly "name taken → suggested alternatives" UX lives in
# the Portal project-creation flow (Plan 4 / R3), not this tier.
# ---------------------------------------------------------------------------

resource "vault_namespace" "project" {
  path = var.project_name
}

# Per-namespace Nomad↔Vault WIF trust anchor. Auth methods do not cross
# namespaces, so each project namespace gets its own jwt-nomad backend. The
# Vault server is co-located with Nomad and reaches the JWKS over loopback; the
# Nomad cert carries a 127.0.0.1 SAN so jwks_ca_pem validation passes.
resource "vault_jwt_auth_backend" "nomad" {
  namespace   = vault_namespace.project.path
  path        = "jwt-nomad"
  type        = "jwt"
  description = "Nomad workload-identity federation (project ${var.project_name})"
  jwks_url    = "https://127.0.0.1:4646/.well-known/jwks.json"
  jwks_ca_pem = local.f.nomad_ca_pem
}
