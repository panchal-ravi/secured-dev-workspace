# Demo Postgres jobspec (Vault use case B1). Synthetic seed data — no real PII.
job "demo-db" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  type        = "service"

  group "db" {
    count = 1

    network {
      port "pg" {
        static = ${db_port}
        to     = 5432
      }
    }

    task "postgres" {
      driver = "docker"

      config {
        image = "postgres:16-alpine"
        ports = ["pg"]
        mount {
          type   = "bind"
          source = "local/seed.sql"
          target = "/docker-entrypoint-initdb.d/seed.sql"
        }
      }

      env {
        POSTGRES_USER     = "${db_user}"
        POSTGRES_PASSWORD = "${db_pass}"
        POSTGRES_DB       = "${db_name}"
      }

      # Seed sample data on first init (no volume => runs every start).
      template {
        destination = "local/seed.sql"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOSQL
CREATE TABLE IF NOT EXISTS employees (
  id     SERIAL PRIMARY KEY,
  name   TEXT NOT NULL,
  role   TEXT NOT NULL,
  salary INTEGER NOT NULL
);
INSERT INTO employees (name, role, salary) VALUES
  ('Ada Lovelace',  'Engineer', 120000),
  ('Alan Turing',   'Architect', 150000),
  ('Grace Hopper',  'Director',  180000);
EOSQL
      }

      # Readiness: the alloc is "healthy" only once Postgres accepts TCP — so the
      # Vault connection resource (depends_on this job) does not race the DB.
      # provider = "nomad": this cluster runs Consul dormant, so the default
      # Consul service provider would add a `consul.version` constraint that
      # excludes the only node. Nomad-native services run the tcp check client-side.
      service {
        name     = "demo-db"
        provider = "nomad"
        port     = "pg"
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
