# ---------------------------------------------------------------------------
# DeepSeek API key for the project's workspaces. The baked-in Claude Code CLI is
# pointed at DeepSeek's Anthropic-compatible endpoint (image managed-settings.json);
# the key itself is delivered the same way as every other workspace secret: written
# to Vault KV here, rendered per session to the `/secrets` tmpfs by the job template,
# and read by Claude's apiKeyHelper — never persisted to /home/dev.
# ---------------------------------------------------------------------------

# One static key per project, stored in the same KV v2 mount the job templates use.
resource "vault_kv_secret_v2" "deepseek" {
  mount = local.f.kv_mount_path
  name  = "projects/${var.project_name}/deepseek"
  data_json = jsonencode({
    api_key = var.deepseek_api_key
  })
}

# WIF grant: the workspace task (via its per-project WIF role) may READ the key.
# KV v2 ⇒ the read path is prefixed with /data/ (consul-template reads it there).
resource "vault_policy" "nomad_deepseek_key" {
  name = "nomad-${var.project_name}-deepseek-key"

  policy = <<-HCL
    path "${local.f.kv_mount_path}/data/projects/${var.project_name}/deepseek" {
      capabilities = ["read"]
    }
  HCL
}
