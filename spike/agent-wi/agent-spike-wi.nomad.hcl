# spike/agent-wi/agent-spike-wi.nomad.hcl — THROWAWAY (Phase 0, spike 3).
# Job NAME must start "agent-" to match the jwt-nomad role's bound_claims job glob.
# Runs in the demo project namespace; just sleeps so the operator can pull the
# workload-identity JWT out of /secrets via `nomad alloc exec`.
#
# Replace REPLACE_PROJECT_NS with the live demo project namespace before running.
# (Negative test: copy this to a job named "spike-wi-neg" — no agent- prefix — and
#  confirm auth/jwt-nomad/login is rejected.)
# After spike 5 nodes exist, add `node_pool = "agents"` at group scope to prove WIF
# from an agent node. Purge: nomad job stop -namespace REPLACE_PROJECT_NS -purge agent-spike-wi
job "agent-spike-wi" {
  type        = "batch"
  namespace   = "REPLACE_PROJECT_NS"
  datacenters = ["dc1"]

  group "g" {
    task "sleep" {
      driver = "docker"

      config {
        image   = "busybox:1.36"
        command = "sleep"
        args    = ["600"]
      }

      identity {
        name = "agent_wi"
        aud  = ["vault.io"]
        file = true # -> ${NOMAD_SECRETS_DIR}/nomad_agent_wi.jwt
        ttl  = "1h"
      }

      resources {
        cpu    = 50
        memory = 32
      }
    }
  }
}
