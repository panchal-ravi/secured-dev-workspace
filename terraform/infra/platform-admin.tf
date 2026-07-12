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
# Variables for this feature are declared in variables.tf (Platform Admin group).

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
    # ORPHANED (Phase F): the platform MCP publish plane that wrote
    # infra/mcp-servers/* descriptors was retired (MCP is project-owned now).
    # Kept to avoid a needless policy churn; safe to remove on next cleanup.
    path "${vault_mount.kv.path}/data/infra/mcp-servers/*" {
      capabilities = ["create", "update", "read"]
    }
  HCL
}

# Self-test WIF token for platform reference MCP instances. Some MCP servers need a
# Vault token merely to open an MCP session (e.g. hashicorp/vault-mcp-server fails
# session creation without one), so the platform-admin consumption-mirror Test can't
# discover their tools tokenless. When a Platform Admin ticks "Inject a Vault token"
# on deploy, the portal renders a bare `vault { role }` block on the reference job and
# Nomad mints/injects/renews/revokes VAULT_TOKEN over WIF — the same mechanism the
# project-plane Class C deploy uses. This policy is intentionally POWERLESS: it grants
# only token self-lookup. Real Vault capability comes solely from the project-plane
# Class C WIF token, scoped by the bound blueprint inside the project namespace.
resource "vault_policy" "infra_mcp_selftest" {
  count = local.platform_admin_count
  name  = "infra-mcp-selftest"

  policy = <<-HCL
    path "auth/token/lookup-self" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_mcp_selftest" {
  count                   = local.platform_admin_count
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-mcp-selftest"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.infra_mcp_selftest[0].name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
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

# The portal resolves the MCP + LLM gateway addresses via Nomad-native service
# discovery (a nomadService template in the portal jobspec) instead of loopback, so
# it no longer needs to be co-located with the gateways. With Nomad ACLs enabled the
# portal's workload identity must be allowed to READ service registrations in the
# "infra" namespace (where both gateways run). This job-scoped, read-only policy is
# bound to the developer-portal job's workload identity via job_acl — nothing else
# gains access. (It may overlap the implicit same-namespace workload policy; keeping
# it explicit makes discovery deterministic regardless of that default.)
resource "nomad_acl_policy" "portal_service_discovery" {
  count       = local.platform_admin_count
  name        = "infra-portal-service-discovery"
  description = "Developer Portal WI: read Nomad service registrations in the infra namespace (gateway discovery)."

  rules_hcl = <<-EOT
    namespace "infra" {
      capabilities = ["list-jobs", "read-job"]
    }
  EOT

  job_acl {
    namespace = "infra"
    job_id    = "developer-portal"
  }
}
