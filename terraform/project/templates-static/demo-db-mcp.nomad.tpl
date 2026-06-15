# Per-project demo-db MCP server, exposed over SSE so the platform MCP gateway can
# federate it (POST /gateways). Replaces the stdio postgres-mcp that used to run
# inside every workspace. Rendered by terraform/project/demo-db-mcp.tf templatefile()
# — consul-template "{{ ... }}" is left literal (different delimiter from Terraform).
#
# postgres-mcp reads DATABASE_URI from the env; --transport=sse serves the MCP
# protocol over HTTP at /sse on port 8000. --access-mode=restricted keeps it
# read-only. The DB credential is the project's Vault-DYNAMIC, read-only role,
# rendered to the task env over WIF — ONE connection held by this service for the
# whole project (not per developer session).
job "demo-db-mcp" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  type        = "service"

  group "mcp" {
    count = 1

    network {
      port "sse" {
        static = ${sse_port}
        to     = 8000
      }
    }

    task "postgres-mcp" {
      driver = "docker"

      config {
        image      = "${image}"
        force_pull = true
        ports      = ["sse"]
        args = [
          "--access-mode=restricted",
          "--transport=sse",
        ]
      }

      # WIF: the project's Nomad↔Vault role (its policy allows reading the read-only
      # DB creds path). Same role the workspace job uses.
      vault {
        namespace = "${vault_namespace}"
        role      = "${wif_role}"
      }

      # Per-project, read-only, Vault-DYNAMIC Postgres credential -> task env as
      # DATABASE_URI. env=true sources the rendered file; change_mode=restart so a
      # lease rotation restarts the MCP server with the fresh credential. The
      # credential is shared by the project (this service holds one connection),
      # not minted per developer session.
      template {
        destination = "secrets/db.env"
        env         = true
        change_mode = "restart"
        data        = <<EOH
{{ with secret "${db_creds_path}" }}DATABASE_URI=postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_endpoint}/appdb?sslmode=disable{{ end }}
EOH
      }

      # provider = "nomad": this cluster runs Consul dormant. tcp check — the SSE
      # port accepting TCP is enough readiness for the gateway to register the peer.
      service {
        name     = "demo-db-mcp"
        provider = "nomad"
        port     = "sse"
        check {
          type     = "tcp"
          interval = "10s"
          timeout  = "2s"
        }
      }

      resources {
        cpu    = 200
        memory = 256
      }
    }
  }
}
