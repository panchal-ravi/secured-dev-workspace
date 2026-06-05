# ---------------------------------------------------------------------------
# Throwaway demo Postgres for Vault use case B1 (per-session read-only DB creds
# behind a Postgres MCP server). Seeded with sample data on every (re)start —
# NO persistent volume, so it re-inits clean. Binds a node-local static host
# port; NOT on the NLB. Vault (on the host) connects at 127.0.0.1:<port> as the
# Postgres superuser to mint short-lived SELECT-only roles. Deliberately
# separate from Boundary's internal Postgres — an agent never touches that.
# ---------------------------------------------------------------------------

locals {
  demo_db_port = 15432
  demo_db_name = "appdb"
  demo_db_user = "vaultadmin" # Postgres superuser Vault uses to create dynamic roles
}

# Admin password Vault uses for its connection. Alphanumeric so it is safe to
# embed in the postgresql:// connection URL (no escaping). Stored (sensitive) in
# this project's state and passed to the container env + the Vault connection.
resource "random_password" "demo_db_admin" {
  length  = 24
  special = false
}

# NOTE: templatefile() renders db_pass into nomad_job.demo_db's "jobspec" state
# attribute, which the Nomad provider does NOT mark sensitive — so the admin
# password appears in plain text in this project's state (in addition to the
# sensitive random_password.demo_db_admin). Acceptable for a throwaway demo DB;
# do not copy this pattern to production.
resource "nomad_job" "demo_db" {
  detach           = false # wait for the alloc to become healthy on apply
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates-static/demo-db.nomad.tpl", {
    namespace = nomad_namespace.project.name
    db_port   = local.demo_db_port
    db_name   = local.demo_db_name
    db_user   = local.demo_db_user
    db_pass   = random_password.demo_db_admin.result
  })

  depends_on = [nomad_namespace.project]
}
