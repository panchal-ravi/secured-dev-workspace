# ---------------------------------------------------------------------------
# Platform Admin onboarding plane (additive, gated). When enabled, the Developer
# Portal gains a Platform-Admin surface to DEPLOY existing MCP servers as Nomad
# jobs and ONBOARD LLM models into the LiteLLM gateway, each verified the way a
# Project Admin consumes it (a ContextForge virtual server + scoped token; a scoped
# budgeted/rate-limited LiteLLM key). This file adds only the platform-wide pieces;
# the portal job env + Vault-policy attach live in developer-portal.tf, gated on the
# same variable, and the LiteLLM STORE_MODEL_IN_DB flip lives in llm-gateway.tf.
#
# Off by default — existing developer flows are byte-identical until an operator
# opts in (and the portal degrades gracefully if the plane can't initialize).
# ---------------------------------------------------------------------------

variable "enable_platform_admin" {
  description = "Enable the Platform Admin onboarding plane (MCP-server deploy + LLM-model onboarding in the portal). Requires enable_developer_portal and the MCP + LLM gateways; flips LiteLLM to STORE_MODEL_IN_DB and mints a portal-admin key."
  type        = bool
  default     = false
}

variable "platform_admin_mcp_node_pool" {
  description = "Node pool for portal-deployed MCP servers. Defaults to \"agents\" (needs enable_agent_nodes); set to \"\" to run them on the all-in-one node instead."
  type        = string
  default     = "agents"
}

locals {
  platform_admin_count = var.enable_platform_admin ? 1 : 0
}

# Dedicated namespace for platform-deployed MCP servers (ContextForge federates
# them; the portal registers Nomad jobs here with the management token).
resource "nomad_namespace" "infra_mcp" {
  count       = local.platform_admin_count
  name        = "infra-mcp"
  description = "Platform-deployed MCP servers (Platform Admin onboarding plane)."
}

# Extra Vault access the portal's admin plane needs, attached to the portal's WIF
# role in developer-portal.tf: read the MCP gateway admin-JWT signing key + admin
# email and the LiteLLM portal-admin key; read/write provider keys; write published
# MCP-server descriptors. The LiteLLM master key is deliberately NOT readable here.
resource "vault_policy" "infra_platform_admin" {
  count = local.platform_admin_count
  name  = "infra-platform-admin"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/mcp-gateway" {
      capabilities = ["read"]
    }
    path "${vault_mount.kv.path}/data/infra/llm-gateway" {
      capabilities = ["read"]
    }
    path "${vault_mount.kv.path}/data/infra/llm-providers/*" {
      capabilities = ["create", "update", "read"]
    }
    path "${vault_mount.kv.path}/data/infra/mcp-servers/*" {
      capabilities = ["create", "update", "read"]
    }
  HCL
}

# Mint the portal-admin (non-master) LiteLLM key once the gateway is up and store it
# in the llm-gateway KV secret. Re-runs if the gateway job is replaced. The portal
# degrades gracefully (admin plane disabled, developer flows unaffected) if this has
# not run yet, so it never blocks a deploy.
resource "terraform_data" "litellm_portal_admin_key" {
  count = local.platform_admin_count

  triggers_replace = [nomad_job.litellm_gateway.id]

  provisioner "local-exec" {
    command = "${path.module}/scripts/litellm-portal-admin-key.sh"
    environment = {
      GW_URL      = "http://${module.secured_codespace.nlb_dns_name}:${local.litellm_port}"
      MASTER_KEY  = "sk-${random_password.litellm_master.result}"
      VAULT_ADDR  = module.secured_codespace.vault_addr
      VAULT_TOKEN = module.secured_codespace.vault_root_token
      KV_MOUNT    = vault_mount.kv.path
      KV_NAME     = "infra/llm-gateway"
    }
  }

  depends_on = [nomad_job.litellm_gateway, vault_kv_secret_v2.llm_gateway]
}
