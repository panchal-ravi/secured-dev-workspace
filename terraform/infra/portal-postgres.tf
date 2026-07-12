# ---------------------------------------------------------------------------
# Postgres backing the Developer Portal's Platform Admin onboarding plane. The
# admin plane persists its control plane here (mcp_servers, llm_models,
# audit_events, blueprints); only metadata/references are stored — secret material
# stays in Vault/LiteLLM. Without this the portal degrades to an in-memory store
# (onboarding state + audit trail lost on restart), so it is part of the
# platform-admin slice and gated on the same switch.
#
# Mirrors the litellm-postgres pattern (llm-gateway.tf): a small dedicated Postgres
# as a Nomad job in the shared "infra" namespace, password generated here, stored in
# Vault KV, read by the job over WIF at start; a persistent host volume so state
# survives a restart. The portal reads the same password to assemble PORTAL_DB_DSN
# (developer-portal.tf / its jobspec). The DB gets its OWN minimal secret + WIF role
# so the database container never sees the portal's privileged provisioning creds.
#
# Off unless BOTH the portal and the platform-admin plane are enabled — a plain
# developer-portal deploy is byte-identical (no DB, in-memory store).
# ---------------------------------------------------------------------------

locals {
  portal_pg_port  = 15434 # node-static Postgres port (15432 demo-db, 15433 litellm-pg)
  portal_pg_count = var.enable_developer_portal && var.enable_platform_admin ? 1 : 0
}

resource "random_password" "portal_pg" {
  count   = local.portal_pg_count
  length  = 32
  special = false
}

# The DB password in its OWN KV secret (NOT the portal's infra/developer-portal
# bundle, which also holds Boundary admin / Nomad mgmt creds). Both the Postgres job
# and the portal read this same password; the portal's read is granted via its
# existing infra-developer-portal secret, where the password is also copied.
resource "vault_kv_secret_v2" "portal_postgres" {
  count = local.portal_pg_count
  mount = vault_mount.kv.path
  name  = "infra/portal-postgres"
  data_json = jsonencode({
    pg_password = random_password.portal_pg[0].result
  })
}

# WIF read policy + role: the Postgres job (Nomad workload identity) may read ONLY
# its own password path. Attaches to the shared jwt-nomad backend, same shape as the
# LiteLLM Postgres role; bound_audiences must match the agent default_identity.aud.
resource "vault_policy" "infra_portal_pg_read" {
  count = local.portal_pg_count
  name  = "infra-portal-postgres-read"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/portal-postgres" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_portal_pg" {
  count                   = local.portal_pg_count
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-portal-postgres"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.infra_portal_pg_read[0].name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
}

# Persistent backing store. Dynamic host volume on the default pool — the portal +
# its DB run on the all-in-one node. Same mechanism + durability caveat as the
# LiteLLM Postgres volume (survives stop/start + reboot, NOT instance replacement).
resource "nomad_dynamic_host_volume" "portal_pg" {
  count     = local.portal_pg_count
  name      = "portal-pg-data"
  namespace = nomad_namespace.infra.name
  plugin_id = "mkdir"
  node_pool = "default"

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }
}

# detach=false waits for the alloc to become healthy on apply so the portal's schema
# migration (it pings + applies CREATE TABLE IF NOT EXISTS on start) does not race an
# unready database.
resource "nomad_job" "portal_postgres" {
  count            = local.portal_pg_count
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/portal-postgres.nomad.hcl.tftpl", {
    namespace = nomad_namespace.infra.name
    image     = var.portal_postgres_image
    port      = local.portal_pg_port
    volume    = nomad_dynamic_host_volume.portal_pg[0].name
    wif_role  = vault_jwt_auth_backend_role.infra_portal_pg[0].role_name
    kv_path   = "${vault_mount.kv.path}/data/infra/portal-postgres"
  })

  depends_on = [nomad_dynamic_host_volume.portal_pg, vault_kv_secret_v2.portal_postgres]
}
