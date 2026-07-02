# ---------------------------------------------------------------------------
# Platform-tier LiteLLM AI Gateway. A single SHARED gateway, deployed ONCE by the
# platform admin, that every project's workspaces send their Claude Code traffic
# through (Anthropic /v1/messages in, DeepSeek out). It holds the ONE real provider
# key; workspaces authenticate with a per-project LiteLLM virtual key (minted by the
# project tier, see terraform/project/llm-gateway.tf) and never see the provider key.
#
# Why this exists: it closes the "ungoverned LLM channel" gap — central audit of
# every prompt/response (spend logs), per-project budgets + rate limits (virtual
# keys), and a one-line swap from DeepSeek to watsonx.ai (the config.yaml model_list).
#
# Mirrors the ContextForge MCP gateway pattern (mcp-gateway.tf): runs as Nomad jobs
# in the shared "infra" namespace; secrets are generated here, stored in Vault KV,
# and read by the jobs over WIF at start. Plaintext HTTP on :4000 (no TLS — PoC; the
# NLB listener is locked to the operator /32, workspaces reach it node-private).
# Virtual keys + budgets + spend/audit logs require Postgres, so a small dedicated
# Postgres runs alongside (litellm-postgres.nomad.hcl.tftpl).
# ---------------------------------------------------------------------------

locals {
  litellm_port    = 4000  # gateway HTTP (Anthropic /v1/messages, admin /key/*, /v1/models)
  litellm_pg_port = 15433 # node-static Postgres port (15432 is the project demo-db)

  # Governed LLM models — the SINGLE source of the model names. Shared by the LiteLLM
  # model_list (rendered below), the portal env (developer-portal.tf), and thus the
  # per-project virtual keys' allowed set + the base templates' Claude Code model
  # mapping. primary = opus/sonnet slot, fast = haiku/subagent slot. Swap DeepSeek for
  # another backend by changing llm_model_backend here.
  llm_model_primary = "deepseek-v4-pro"
  llm_model_fast    = "deepseek-v4-flash"
  llm_model_backend = "deepseek/deepseek-chat"
  llm_model_names   = [local.llm_model_primary, local.llm_model_fast]
}

# Gateway secrets. 48-char alphanumerics (no special) so they are safe in an env
# file with no escaping. The master key must start with "sk-".
resource "random_password" "litellm_master" {
  length  = 48
  special = false
}

resource "random_password" "litellm_salt" {
  length  = 48
  special = false
}

resource "random_password" "litellm_pg" {
  length  = 32
  special = false
}

# Stored in the same KV v2 mount the MCP gateway + project job templates use, under
# the infra/ prefix. Both gateway jobs read this over WIF; the project tier reads
# master_key from here (via its root vault provider) to mint per-project virtual keys.
resource "vault_kv_secret_v2" "llm_gateway" {
  mount = vault_mount.kv.path
  name  = "infra/llm-gateway"
  data_json = jsonencode({
    master_key       = "sk-${random_password.litellm_master.result}" # proxy admin key (mints virtual keys)
    salt_key         = random_password.litellm_salt.result           # encrypts stored credentials
    pg_password      = random_password.litellm_pg.result             # Postgres password (DB-backed keys/logs)
    deepseek_api_key = var.deepseek_api_key                          # the ONE provider key, never reaches a workspace
  })

  # portal_admin_key is minted + merge-patched into this secret OUT-OF-BAND by
  # scripts/litellm-portal-admin-key.sh (terraform_data.litellm_portal_admin_key).
  # Without this, every plan tries to revert data_json to the four keys above and
  # DROP that out-of-band portal_admin_key — breaking the portal's LLM onboarding.
  # The four managed keys are generated/stable, so suppressing post-create drift on
  # the blob is safe (a key rotation is a deliberate taint/replace, not a silent edit).
  lifecycle {
    ignore_changes = [data_json]
  }
}

# WIF read policy + role: the gateway + Postgres jobs (Nomad workload identity) may
# read ONLY this secret path. Attaches to the shared jwt-nomad backend, same as the
# MCP role; bound_audiences must match the agent default_identity.aud.
resource "vault_policy" "infra_llm_read" {
  name = "infra-llm-gateway-read"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/llm-gateway" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_llm" {
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-llm-gateway"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.infra_llm_read.name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
}

# Persistent backing store for LiteLLM's Postgres (virtual keys + spend/audit logs
# must survive a restart). Dynamic host volume on the default pool — the gateway runs
# on the all-in-one node. Same mechanism as the MCP gateway's SQLite volume. NOTE:
# lives on the node root volume — survives stop/start + reboot, NOT an instance
# replacement (durable storage is the roadmap fix, shared with the MCP gateway).
resource "nomad_dynamic_host_volume" "litellm_pg" {
  name      = "litellm-pg-data"
  namespace = nomad_namespace.infra.name
  plugin_id = "mkdir"
  node_pool = "default"

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }
}

# Postgres for LiteLLM. detach=false waits for the alloc to become healthy on apply
# so the gateway's DB migrations don't race an unready database.
resource "nomad_job" "litellm_postgres" {
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/litellm-postgres.nomad.hcl.tftpl", {
    namespace = nomad_namespace.infra.name
    image     = var.litellm_postgres_image
    port      = local.litellm_pg_port
    volume    = nomad_dynamic_host_volume.litellm_pg.name
    wif_role  = vault_jwt_auth_backend_role.infra_llm.role_name
    kv_path   = "${vault_mount.kv.path}/data/infra/llm-gateway"
  })

  depends_on = [nomad_dynamic_host_volume.litellm_pg, vault_kv_secret_v2.llm_gateway]
}

# LiteLLM proxy. Reads master/salt/provider-key + the DB password over WIF, runs the
# Prisma migrations against Postgres on start (litellm-database image), and serves the
# Anthropic /v1/messages endpoint workspaces point Claude Code at. depends_on Postgres
# so the migration finds a live DB; the task restart policy covers a slow DB start.
resource "nomad_job" "litellm_gateway" {
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/litellm.nomad.hcl.tftpl", {
    namespace     = nomad_namespace.infra.name
    image         = var.litellm_image
    port          = local.litellm_port
    wif_role      = vault_jwt_auth_backend_role.infra_llm.role_name
    kv_path       = "${vault_mount.kv.path}/data/infra/llm-gateway"
    db_host       = "${module.secured_codespace.instance_private_ip}:${local.litellm_pg_port}"
    model_primary = local.llm_model_primary
    model_fast    = local.llm_model_fast
    model_backend = local.llm_model_backend
    # Enable DB-stored models so the Platform Admin plane can add/manage models via
    # the admin API. Off by default — config-list models are unchanged either way.
    store_model_in_db = var.enable_platform_admin
  })

  depends_on = [nomad_job.litellm_postgres, vault_kv_secret_v2.llm_gateway]
}
