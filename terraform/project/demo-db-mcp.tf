# ---------------------------------------------------------------------------
# Per-project demo-db MCP server (SSE). postgres-mcp run as a long-lived Nomad
# service exposed over SSE so the platform MCP gateway can federate it as a peer
# (terraform/project/mcp-gateway.tf). This replaces the stdio postgres-mcp that
# used to be embedded in every workspace image. It connects to the demo-db with
# the project's Vault-dynamic, read-only role over WIF (one shared connection for
# the project; auto-rotated on lease — see database.tf).
# ---------------------------------------------------------------------------

locals {
  demo_db_mcp_port   = 18080 # node-static SSE port the gateway dials
  postgres_mcp_image = "crystaldba/postgres-mcp:0.3.0"
}

resource "nomad_job" "demo_db_mcp" {
  detach           = false # wait for the alloc to become healthy on apply
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates-static/demo-db-mcp.nomad.tpl", {
    namespace       = nomad_namespace.project.name
    vault_namespace = vault_namespace.project.path
    image           = local.postgres_mcp_image
    sse_port        = local.demo_db_mcp_port
    wif_role        = vault_jwt_auth_backend_role.project.role_name
    db_creds_path   = "${vault_mount.database.path}/creds/${vault_database_secret_backend_role.dev_workspace_ro.name}"
    db_endpoint     = "${local.f.instance_private_ip}:${local.demo_db_port}"
  })

  # The DB role/connection must exist (so the WIF read works) and the Postgres job
  # must be placed before this service tries to connect.
  depends_on = [
    vault_database_secret_backend_role.dev_workspace_ro,
    nomad_job.demo_db,
  ]
}
