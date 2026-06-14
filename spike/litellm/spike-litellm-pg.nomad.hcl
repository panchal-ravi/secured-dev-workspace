# spike/litellm/spike-litellm-pg.nomad.hcl — THROWAWAY (Phase 0, spike 4).
# Scratch Postgres for the scratch LiteLLM. ns "spike", static port 15435
# (15432/15433/15434 taken/reserved), EPHEMERAL storage (no host volume — dies with
# the job). The REAL litellm-pg (:15433) is never touched.
# Replace REPLACE_PG_PASSWORD with a throwaway random password (must match the
# DATABASE_URL in spike-litellm.nomad.hcl). initdb under uid 70 needs no host-vol
# chown here because the data dir is the container's own ephemeral fs.
# Purge: nomad job stop -namespace spike -purge spike-litellm-pg
job "spike-litellm-pg" {
  namespace   = "spike"
  datacenters = ["dc1"]
  type        = "service"

  group "db" {
    count = 1

    network {
      port "pg" {
        static = 15435
        to     = 5432
      }
    }

    task "postgres" {
      driver = "docker"

      config {
        image = "postgres:16"
        ports = ["pg"]
      }

      env {
        POSTGRES_USER     = "litellm"
        POSTGRES_DB       = "litellm"
        POSTGRES_PASSWORD = "REPLACE_PG_PASSWORD"
      }

      service {
        name     = "spike-litellm-postgres"
        provider = "nomad"
        port     = "pg"
        check {
          type     = "tcp"
          interval = "10s"
          timeout  = "2s"
        }
      }

      resources {
        cpu    = 250
        memory = 512
      }
    }
  }
}
