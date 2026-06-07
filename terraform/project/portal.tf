# ---------------------------------------------------------------------------
# Developer Portal descriptor. Publishes ONE document per project to Vault KV at
# secret/projects/<project>/portal-descriptor that the Developer Portal reads to
# render project cards, gate access (developers_group_name vs the logged-in
# developer's IBM Verify groups), and drive direct-API workspace creation
# (the same facts the dev-workspace Terraform tier reads from this state).
#
# The portal LISTs secret/metadata/projects/ to enumerate projects, then reads
# each project's portal-descriptor. The future "Project Team owner" persona will
# write this same descriptor when it creates a project, so the schema is the
# contract between the project tier and the portal.
# ---------------------------------------------------------------------------

locals {
  # Per-flavor feature metadata shown in the workspace card's info section. Keyed
  # by job-template name. Every feature below is unconditionally wired into the
  # dev-workspace template (see templates/dev-workspace.nomad.hcl); add an entry
  # when a new flavor is added under templates/.
  flavor_features = {
    "dev-workspace" = [
      {
        key         = "claude-deepseek"
        label       = "Claude Code CLI (governed model)"
        description = "Pre-configured AI coding assistant. The API key is injected per session from Vault and never lands on the persistent home volume."
      },
      {
        key         = "db-mcp-readonly"
        label       = "Database MCP (read-only)"
        description = "A read-only Postgres MCP server, federated through the central ContextForge MCP gateway as this project's virtual MCP server. Backed by a Vault-dynamic, read-only database credential."
      },
      {
        key         = "git-dynamic-pat"
        label       = "Git push (dynamic PAT)"
        description = "git is pre-configured with your identity and a short-lived GitHub App token as the push credential — no static PAT anywhere."
      },
    ]
    "gpu-workspace" = [
      {
        key         = "nvidia-t4-gpu"
        label       = "NVIDIA T4 GPU"
        description = "Scheduled on a GPU node (g4dn.xlarge, NVIDIA T4). nvidia-smi and nvcc are available; a CUDA vectorAdd sample is included in the repo."
      },
      {
        key         = "claude-deepseek"
        label       = "Claude Code CLI (governed model)"
        description = "Pre-configured AI coding assistant. The API key is injected per session from Vault and never lands on the persistent home volume."
      },
      {
        key         = "db-mcp-readonly"
        label       = "Database MCP (read-only)"
        description = "A read-only Postgres MCP server, federated through the central ContextForge MCP gateway as this project's virtual MCP server. Backed by a Vault-dynamic, read-only database credential."
      },
      {
        key         = "git-dynamic-pat"
        label       = "Git push (dynamic PAT)"
        description = "git is pre-configured with your identity and a short-lived GitHub App token as the push credential — no static PAT anywhere."
      },
    ]
  }

  # The descriptor carries what the portal needs that is NOT a Vault-secret layout:
  # Boundary provisioning IDs, the access-control group, the node address, and
  # per-flavor metadata for the template picker (label/description/repo/image/
  # node_pool/features). The Vault paths + DB endpoint stay baked into the published
  # job template (kv.tf); repo/image are surfaced here only for display + to let the
  # portal place GPU flavors (node_pool).
  portal_descriptor = {
    project_name                 = var.project_name
    namespace                    = nomad_namespace.project.name
    project_scope_id             = boundary_scope.project.id
    credential_library_id        = boundary_credential_library_vault_ssh_certificate.this.id
    developers_group_name        = var.developers_group_name
    boundary_oidc_auth_method_id = local.f.boundary_oidc_auth_method_id
    instance_private_ip          = local.f.instance_private_ip
    workspace_user               = var.workspace_user
    alias_suffix                 = "boundary"

    # One entry per published job template ("flavor"): name (the portal reads that
    # template back), the picker metadata from var.workspace_templates, and the
    # features the card surfaces. Iterates the published set so the descriptor lists
    # exactly what was published. node_pool drives node placement (e.g. "gpu").
    flavors = [
      for name in keys(vault_kv_secret_v2.job_template) : {
        name         = name
        label        = coalesce(var.workspace_templates[name].label, name)
        description  = coalesce(var.workspace_templates[name].description, "")
        git_repo_url = var.workspace_templates[name].git_repo_url
        image        = var.workspace_templates[name].image
        node_pool    = var.workspace_templates[name].node_pool
        features     = lookup(local.flavor_features, name, [])
      }
    ]
  }
}

resource "vault_kv_secret_v2" "portal_descriptor" {
  mount     = local.f.kv_mount_path
  name      = "projects/${var.project_name}/portal-descriptor"
  data_json = jsonencode({ descriptor = jsonencode(local.portal_descriptor) })
}
