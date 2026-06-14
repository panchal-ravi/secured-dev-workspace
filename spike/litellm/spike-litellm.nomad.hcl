# spike/litellm/spike-litellm.nomad.hcl — THROWAWAY (Phase 0, spike 4).
# Scratch LiteLLM gateway in DB mode. ns "spike", static port 14000. Proves
# STORE_MODEL_IN_DB model CRUD coexisting with a static config.yaml model, and the
# per-instance /key/generate (TTL+budget)->revoke pattern. The REAL gateway (:4000)
# is never touched.
#
# Before running, replace:
#   REPLACE_IMAGE              -> the SAME image tag as the deployed gateway (var.litellm_image,
#                                pinned — findings R8) to dodge admin-API schema drift.
#   REPLACE_MASTER_KEY         -> sk-spike-$(openssl rand -hex 16)
#   REPLACE_SALT_KEY           -> sk-spike-salt-$(openssl rand -hex 16)
#   REPLACE_DEEPSEEK_API_KEY   -> vault kv get -field=deepseek_api_key secret/infra/llm-gateway
#   REPLACE_ALLINONE_PRIVATE_IP-> the node-private IP where spike-litellm-pg's :15435 is reachable
# Verify STORE_MODEL_IN_DB env vs general_settings.store_model_in_db for the pinned
# image (findings R9). Purge: nomad job stop -namespace spike -purge spike-litellm
job "spike-litellm" {
  namespace   = "spike"
  datacenters = ["dc1"]
  type        = "service"

  group "gateway" {
    count = 1

    restart {
      attempts = 5
      interval = "5m"
      delay    = "15s"
      mode     = "delay"
    }

    network {
      port "http" {
        static = 14000
        to     = 14000
      }
    }

    task "gateway" {
      driver = "docker"

      config {
        image      = "REPLACE_IMAGE"
        force_pull = true
        ports      = ["http"]
        args       = ["--config", "/local/config.yaml", "--host", "0.0.0.0", "--port", "14000"]
      }

      env {
        LITELLM_MASTER_KEY = "REPLACE_MASTER_KEY"
        LITELLM_SALT_KEY   = "REPLACE_SALT_KEY"
        DEEPSEEK_API_KEY   = "REPLACE_DEEPSEEK_API_KEY"
        DATABASE_URL       = "postgresql://litellm:REPLACE_PG_PASSWORD@REPLACE_ALLINONE_PRIVATE_IP:15435/litellm"
        STORE_MODEL_IN_DB  = "True"
      }

      # One STATIC model so coexistence with DB-managed models is testable.
      template {
        destination = "local/config.yaml"
        change_mode = "restart"
        data        = <<EOH
model_list:
  - model_name: spike-static-model
    litellm_params:
      model: deepseek/deepseek-chat
      api_key: os.environ/DEEPSEEK_API_KEY

litellm_settings:
  drop_params: true

general_settings:
  master_key: os.environ/LITELLM_MASTER_KEY
  store_model_in_db: true
EOH
      }

      service {
        name     = "spike-litellm"
        provider = "nomad"
        port     = "http"
        check {
          type     = "http"
          path     = "/health/liveliness"
          interval = "10s"
          timeout  = "2s"
        }
      }

      resources {
        cpu    = 1000
        memory = 2048
      }
    }
  }
}
