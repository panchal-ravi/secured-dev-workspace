# ---------------------------------------------------------------------------
# Per-project virtual key in the shared LiteLLM AI gateway. For each project the
# project-admin mints ONE LiteLLM virtual key — scoped to the allowed models, a
# budget, and a rate limit — and writes {base_url, virtual_key} to Vault KV
# (secret/projects/<project>/llm). The workspace reads the key over WIF and Claude
# Code presents it to the gateway; the real provider (DeepSeek) key never leaves the
# gateway (terraform/infra/llm-gateway.tf, platform tier).
#
# Key generation is a runtime call to the gateway admin API (the key string is only
# known after POST /key/generate), so it runs in scripts/llm-provision.sh via a
# terraform_data create provisioner; a destroy-time provisioner revokes the key and
# removes the KV path. Mirrors the MCP gateway orchestration (mcp-gateway.tf). All
# inputs (incl. the master key + Vault token) are passed via the ENVIRONMENT, never
# the command line, so secrets are not logged.
# ---------------------------------------------------------------------------

locals {
  llm_key_alias = "llm-${var.project_name}" # stable per-project alias (idempotent re-issue)
  llm_kv_name   = "projects/${var.project_name}/llm"
  llm_models    = "deepseek-v4-pro,deepseek-v4-flash" # must match the gateway model_list + managed-settings
  # Per-project guardrails on the virtual key (PoC values; tune per project).
  llm_max_budget = 50  # USD soft cap over the key's lifetime
  llm_rpm_limit  = 120 # requests/minute
}

# The gateway's master key (written by the infra tier). Used to authenticate the
# /key/generate and /key/delete admin calls.
data "vault_kv_secret_v2" "llm_gateway" {
  mount = local.f.kv_mount_path
  name  = "infra/llm-gateway"
}

# WIF grant: the workspace task (via its per-project WIF role) may READ the virtual
# key. KV v2 ⇒ the read path is prefixed with /data/. Attached to the project WIF
# role's token_policies in vault.tf (replaces the old DeepSeek-key read policy).
resource "vault_policy" "nomad_llm_read" {
  namespace = vault_namespace.project.path
  name      = "nomad-${var.project_name}-llm-read"

  policy = <<-HCL
    path "${local.f.kv_mount_path}/data/projects/${var.project_name}/llm" {
      capabilities = ["read"]
    }
  HCL
}

resource "terraform_data" "llm_provision" {
  # Re-run when the gateway endpoint, the model scope, or the alias changes.
  triggers_replace = jsonencode({
    base_url  = local.f.llm_gateway_private_endpoint
    key_alias = local.llm_key_alias
    models    = local.llm_models
  })

  # Stashed in state so the destroy-time provisioner (which cannot read data
  # sources/locals) still has everything it needs. This project's state is already
  # secret-bearing and gitignored.
  input = {
    GW_ADMIN_URL = local.f.llm_gateway_addr             # NLB:4000 (operator /32) — admin key calls
    GW_BASE_URL  = local.f.llm_gateway_private_endpoint # node-ip:4000 — what the workspace uses
    PROJECT      = var.project_name
    KEY_ALIAS    = local.llm_key_alias
    MODELS       = local.llm_models
    MAX_BUDGET   = tostring(local.llm_max_budget)
    RPM_LIMIT    = tostring(local.llm_rpm_limit)
    MASTER_KEY   = data.vault_kv_secret_v2.llm_gateway.data["master_key"]
    VAULT_ADDR      = local.f.vault_addr
    VAULT_TOKEN     = local.f.vault_root_token
    KV_MOUNT        = local.f.kv_mount_path
    KV_NAME         = local.llm_kv_name
    VAULT_NAMESPACE = vault_namespace.project.path
  }

  provisioner "local-exec" {
    when        = create
    command     = "bash ${path.module}/scripts/llm-provision.sh provision"
    environment = self.input
  }

  provisioner "local-exec" {
    when        = destroy
    command     = "bash ${path.module}/scripts/llm-provision.sh deprovision"
    environment = self.input
  }

  depends_on = [vault_mount.kv]
}
