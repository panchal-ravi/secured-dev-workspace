# ---------------------------------------------------------------------------
# Demo Postgres — the upstream database the Class A (postgres-mcp) credential
# blueprint brokers against in the MCP E2E. Replaces the retired
# terraform/project demo-db: a small Postgres as a Nomad job in the shared
# "infra" namespace on the default pool (all-in-one node), node-static :15432
# (the instance SG already opens 15432 intra-SG, so Vault on the node and MCP
# jobs on the agents node can both reach it).
#
# Deliberately simpler than portal/litellm Postgres: no host volume (re-seeds on
# every restart — throwaway demo data) and the bootstrap password goes straight
# to the container env + TF outputs. NOTE: the Class A blueprint ROTATES this
# bootstrap password out of human knowledge on first project deploy — the
# demo_db_admin_password output is single-use per database.
# ---------------------------------------------------------------------------

locals {
  demo_db_port  = 15432 # node-static (15433 litellm-pg, 15434 portal-pg)
  demo_db_name  = "appdb"
  demo_db_user  = "vaultadmin" # bootstrap admin the Class A blueprint rotates away
  demo_db_count = var.enable_demo_db ? 1 : 0
}

resource "random_password" "demo_db_admin" {
  count   = local.demo_db_count
  length  = 24
  special = false # URL-safe: no escaping needed in postgresql:// URLs
}

# templatefile() renders the password into this job's state in plain text —
# acceptable for the throwaway demo DB (same caveat as the retired project job).
resource "nomad_job" "demo_db" {
  count            = local.demo_db_count
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/demo-db.nomad.hcl.tftpl", {
    namespace = nomad_namespace.infra.name
    db_port   = local.demo_db_port
    db_name   = local.demo_db_name
    db_user   = local.demo_db_user
    db_pass   = random_password.demo_db_admin[0].result
  })
}
