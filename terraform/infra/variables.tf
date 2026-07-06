variable "owner" {
  description = "Owner tag/prefix; must match the owner used to build the AMI"
  type        = string
  default     = "rp"
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "ap-southeast-1"
}

variable "instance_type" {
  description = "EC2 instance type for the all-in-one Boundary node"
  type        = string
  default     = "t3.large"
}

variable "enable_gpu_node" {
  description = "Provision the GPU Nomad client EC2 (NVIDIA T4). Off by default — the g4dn instance is costly, so opt in only when GPU workspaces are needed."
  type        = bool
  default     = false
}

variable "gpu_instance_type" {
  description = "EC2 instance type for the GPU Nomad client node (NVIDIA T4)"
  type        = string
  default     = "g4dn.xlarge"
}

variable "gpu_root_volume_size" {
  description = "Root EBS volume size (GiB) for the GPU node — large enough for CUDA images"
  type        = number
  default     = 60
}

variable "enable_agent_nodes" {
  description = "Provision the standard-CPU agent worker EC2s (Nomad clients in node pool \"agents\") for the agent-platform tier. Off by default — opt in to schedule agent instances + wrapped MCP servers off the all-in-one node."
  type        = bool
  default     = false
}

variable "agent_node_count" {
  description = "Number of agent worker nodes to provision when enable_agent_nodes = true."
  type        = number
  default     = 1
}

variable "agent_instance_type" {
  description = "EC2 instance type for each agent worker node (standard CPU)."
  type        = string
  default     = "t3.large"
}

variable "agent_root_volume_size" {
  description = "Root EBS volume size (GiB) for each agent worker node."
  type        = number
  default     = 40
}

variable "enable_microvm_node" {
  description = "Provision the Kata microVM Nomad client EC2 (bare metal). Off by default — metal instances are costly, so opt in only when hardware-isolated microVM workspaces are needed."
  type        = bool
  default     = false
}

variable "microvm_instance_type" {
  description = "EC2 instance type for the microVM Nomad client node. MUST be bare metal (e.g. c5.metal) — Kata needs /dev/kvm, which Nitro guests do not expose."
  type        = string
  default     = "c5.metal"
}

variable "microvm_root_volume_size" {
  description = "Root EBS volume size (GiB) for the microVM node — room for the Kata guest kernel/rootfs + images"
  type        = number
  default     = 60
}

variable "boundary_version" {
  description = "Boundary Enterprise version baked into the AMI (informational)"
  type        = string
  default     = "0.21.3+ent"
}

variable "nomad_version" {
  description = "Nomad Enterprise version baked into the AMI (informational)"
  type        = string
  default     = "1.11.6+ent"
}

variable "vault_version" {
  description = "Vault Enterprise version baked into the AMI (informational)"
  type        = string
  default     = "1.20.4+ent"
}

variable "boundary_admin_login_name" {
  description = "Login name for the initial Boundary admin account"
  type        = string
  default     = "admin"
}

variable "boundary_admin_password" {
  description = "Password for the initial Boundary admin account (min 8 chars; avoid single quotes)"
  type        = string
  default     = "Password123!"
  sensitive   = true
}

variable "boundary_org_name" {
  description = "Name of the org scope created under global"
  type        = string
  default     = "primary-org"
}

# --- Identity layer: IBM Verify OIDC SSO (see identity.tf / modules/identity) ---

variable "ibm_verify_tenant" {
  description = "IBM Verify SaaS tenant hostname (no scheme), e.g. myorg.verify.ibm.com"
  type        = string
}

variable "ibm_verify_api_client_id" {
  description = <<-EOT
    Client ID of the bootstrap IBM Verify API client used to manage applications.
    Created once, manually, in the Verify console (Security -> API access).
    Needs entitlements: manageAppAccessAdmin (manage applications) and
    readAppConfigAndClientSecret (read the generated app client secret).
  EOT
  type        = string
}

variable "ibm_verify_api_client_secret" {
  description = "Client secret of the bootstrap IBM Verify API client."
  type        = string
  sensitive   = true
}

variable "admin_group_name" {
  description = "IBM Verify group whose members get ADMIN access in Boundary and Nomad."
  type        = string
  default     = "secured-codespace-admins"
}

variable "readonly_group_name" {
  description = "IBM Verify group whose members get READ-ONLY access in Boundary and Nomad."
  type        = string
  default     = "secured-codespace-readonly"
}

# --- ContextForge MCP Gateway (see mcp-gateway.tf) ---

variable "mcp_gateway_image" {
  description = "ContextForge MCP Gateway container image (amd64). Pin a concrete release tag at apply rather than relying on :latest."
  type        = string
  default     = "ghcr.io/ibm/mcp-context-forge:latest"
}

# --- LiteLLM AI Gateway (see llm-gateway.tf) ---

variable "litellm_image" {
  description = "LiteLLM proxy container image. The `-database` variant runs the Prisma DB migrations on start (needed for virtual keys + spend logs). Pin a concrete release tag at apply rather than relying on :main-stable."
  type        = string
  default     = "ghcr.io/berriai/litellm-database:main-stable"
}

variable "litellm_postgres_image" {
  description = "Postgres image backing the LiteLLM proxy (virtual keys, budgets, spend/audit logs)."
  type        = string
  default     = "postgres:16-alpine"
}

variable "deepseek_api_key" {
  description = <<-EOT
    The ONE central DeepSeek API key the LiteLLM gateway uses to reach DeepSeek.
    Stored only in Vault KV (infra/llm-gateway), read by the gateway job over WIF —
    it never reaches a workspace (workspaces get a per-project LiteLLM virtual key
    instead). Moving the provider key here (from the old per-project project tier)
    is the point: swapping to watsonx.ai later is a one-line model_list change.
    Supply via the gitignored infra tfvars (never commit).
  EOT
  type        = string
  sensitive   = true
}

# --- Developer Portal (see developer-portal.tf) ---

# Deploy the portal as a Nomad job? Off by default: it requires the image pushed
# (portal/scripts/build-image.sh) and portal_oidc_issuer set. When true, the
# identity module ALSO creates the portal's Verify OIDC app (no hand-registration)
# — see modules/identity/verify.tf. The base stack + the NLB `:8443` listener
# provision regardless; flip this true for the portal job + its Verify app.
variable "enable_developer_portal" {
  description = "Deploy the Developer Portal Nomad job AND create its IBM Verify OIDC app (needs the image + portal_oidc_issuer)."
  type        = bool
  default     = false
}

variable "developer_portal_image" {
  description = "Developer Portal container image (amd64). Build/push with portal/scripts/build-image.sh and pin a concrete tag."
  type        = string
  default     = "panchalravi/developer-portal:poc"
}

variable "portal_oidc_issuer" {
  description = "The portal's IBM Verify OIDC issuer (full endpoint, e.g. https://<tenant>.verify.ibm.com/oidc/endpoint/default). Required only when enable_developer_portal = true."
  type        = string
  default     = ""
}

# Access-token audiences stamped on the portal's Verify app. Typically the
# token-exchange client id the RFC 8693 OBO flow targets. The portal's client
# id/secret are NO LONGER vars — the identity module creates the app and surfaces
# them as outputs.
variable "portal_oidc_audiences" {
  description = "Audiences for the portal Verify app's access token (e.g. the token-exchange app's client id)."
  type        = list(string)
  default     = ["7be9262c-f5c3-4174-a105-038a0892699f"]
}

# --- Platform Admin onboarding plane (see platform-admin.tf) ---

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

variable "portal_postgres_image" {
  description = "Postgres image backing the portal's onboarding control plane (mcp_servers, llm_models, audit_events, blueprints). Deployed only when enable_platform_admin = true."
  type        = string
  default     = "postgres:16-alpine"
}

# --- Agent-platform identity (see agent-identity.tf) ---

# Static org-context claims stamped into every actor JWT (overview §3 step 4).
# Values are cosmetic identity context that Verify copies into the OBO `act`
# claim; functionally inert. (Spike 2/3 used ibm/platform/secured-dev ad hoc —
# these spec defaults supersede them.)
variable "agent_identity_claims" {
  type    = object({ org = string, bu = string, department = string, service_group = string })
  default = { org = "ibm-demo", bu = "techsales", department = "advanced-sa", service_group = "agent-platform" }
}

# --- Vault GitHub secrets plugin (see vault-github-plugin.tf) ---

variable "github_plugin_version" {
  description = "vault-plugin-secrets-github release version (no leading v). Must match the AMI's baked binary."
  type        = string
  default     = "2.3.2"
}

variable "github_plugin_sha256" {
  description = "SHA-256 of the baked linux-amd64 plugin binary. MUST match ami/base_image github_plugin_sha256."
  type        = string
  default     = "72cb1f2775ee2abf12ffb725e469d0377fe7bbb93cd7aaa6921c141eddecab87"
}

variable "enable_demo_db" {
  description = "Deploy the throwaway demo Postgres (Nomad job, infra namespace, node-static :15432) used as the upstream DB for the Class A postgres-mcp blueprint E2E. No persistent volume — data re-seeds on every restart."
  type        = bool
  default     = false
}
