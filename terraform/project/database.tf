# ---------------------------------------------------------------------------
# Per-project Vault database secrets engine (database/<project>). Mints
# per-session, SELECT-only Postgres roles against the demo-db, auto-revoked at
# lease expiry. The workspace task reads database/<project>/creds/dev-workspace-ro
# over WIF; the Postgres MCP server connects with that ephemeral user.
# ---------------------------------------------------------------------------

resource "vault_mount" "database" {
  namespace   = vault_namespace.project.path
  path        = "database/${var.project_name}"
  type        = "database"
  description = "Dynamic Postgres credentials (demo-db) for project ${var.project_name}"
}

# Vault reaches the demo-db at the node's private IP (the Nomad docker driver
# publishes the static port on the node's advertised address, NOT loopback), the
# same host:port the workspace container uses. verify_connection is OFF: Postgres
# accepts TCP (the demo-db's only available Nomad-native health check) before it
# is ready for queries, so apply-time verification would race initdb. Vault
# instead verifies on the first credential request. depends_on still gates this
# resource on the job being placed.
resource "vault_database_secret_backend_connection" "demo_db" {
  namespace         = vault_namespace.project.path
  backend           = vault_mount.database.path
  name              = "demo-db"
  allowed_roles     = ["dev-workspace-ro"]
  verify_connection = false

  postgresql {
    # Node-private address (not routable off the node), so no TLS needed.
    connection_url = "postgresql://{{username}}:{{password}}@${local.f.instance_private_ip}:${local.demo_db_port}/${local.demo_db_name}?sslmode=disable"
    username       = local.demo_db_user
    password       = random_password.demo_db_admin.result
  }

  depends_on = [nomad_job.demo_db]
}

# Read-only role: each lease creates a fresh LOGIN role with SELECT on public,
# valid until the lease expires. This credential is now held by the long-lived
# per-project demo-db-mcp service (demo-db-mcp.tf), not minted per developer
# session — so TTLs are bumped well beyond a session: consul-template restarts the
# MCP server (change_mode=restart) when the lease rotates, dropping the old role.
resource "vault_database_secret_backend_role" "dev_workspace_ro" {
  namespace = vault_namespace.project.path
  backend   = vault_mount.database.path
  name    = "dev-workspace-ro"
  db_name = vault_database_secret_backend_connection.demo_db.name

  default_ttl = 86400  # 24h
  max_ttl     = 604800 # 7d

  creation_statements = [
    "CREATE ROLE \"{{name}}\" WITH LOGIN PASSWORD '{{password}}' VALID UNTIL '{{expiration}}';",
    "GRANT USAGE ON SCHEMA public TO \"{{name}}\";",
    "GRANT SELECT ON ALL TABLES IN SCHEMA public TO \"{{name}}\";",
    "ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO \"{{name}}\";",
  ]

  revocation_statements = [
    "ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT ON TABLES FROM \"{{name}}\";",
    "REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM \"{{name}}\";",
    "REVOKE USAGE ON SCHEMA public FROM \"{{name}}\";",
    "DROP ROLE IF EXISTS \"{{name}}\";",
  ]
}
