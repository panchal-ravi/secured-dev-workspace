# ---------------------------------------------------------------------------
# Publish this project's Nomad job templates to Vault KV. Each template the project
# OPTED INTO (a key in var.workspace_templates) is read from templates/<name>.nomad.hcl
# and written to secret/projects/<project>/job-templates/<name> with this project's
# PROJECT-STATIC values rendered in by templatestring() — including the template's OWN
# image and git repo (per-template). The per-workspace placeholders are escaped as
# `$${...}` in the source template, so they survive this pass as literal `${...}` for
# the Developer Portal to fill at create time. Add a template = add a templates/<name>.nomad.hcl
# file AND a workspace_templates entry for it.
# ---------------------------------------------------------------------------

locals {
  job_template_dir = "${path.module}/templates"

  # Read each selected template's source from templates/<name>.nomad.hcl. A file
  # present under templates/ but not listed in workspace_templates is not published.
  job_templates = {
    for name, cfg in var.workspace_templates :
    name => file("${local.job_template_dir}/${name}.nomad.hcl")
  }
}

resource "vault_kv_secret_v2" "job_template" {
  for_each = var.workspace_templates
  mount    = local.f.kv_mount_path
  name     = "projects/${var.project_name}/job-templates/${each.key}"
  # Bake the project-static placeholders now (single source of truth: the project
  # tier owns these resources). Image and repo are per-template (each.value); the
  # Vault paths are project-wide. The workspace no longer talks to Postgres directly
  # — it reads the per-project MCP coordinates (virtual-server URL + client token)
  # from mcp_kv_path and points Claude's REMOTE MCP at the gateway. Claude Code's LLM
  # traffic routes through the shared LiteLLM gateway: llm_base_url is baked into the
  # workspace's managed-settings.json, and llm_kv_path is where it reads the project's
  # virtual key. The portal then fills only the per-workspace placeholders
  # (job_name/ssh_port/volume_name/identity).
  data_json = jsonencode({
    jobspec = templatestring(local.job_templates[each.key], {
      namespace         = nomad_namespace.project.name
      image             = each.value.image
      git_repo_url      = each.value.git_repo_url
      wif_role          = var.project_name
      ssh_ca_path       = "${vault_mount.ssh.path}/config/ca"
      github_token_path = "${vault_mount.github.path}/token/${local.github_permissionset_name}"
      mcp_kv_path       = "${local.f.kv_mount_path}/data/projects/${var.project_name}/mcp"
      llm_kv_path       = "${local.f.kv_mount_path}/data/projects/${var.project_name}/llm"
      llm_base_url      = local.f.llm_gateway_private_endpoint
    })
  })
}
