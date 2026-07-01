# ---------------------------------------------------------------------------
# Project-onboarding inputs (flat root, instantiated ONCE PER PROJECT via
# `terraform workspace new <project>`). It carves out a project's slice across
# all three control planes — a Boundary project scope, a Nomad namespace, and a
# path-prefixed Vault SSH CA — plus the Boundary Vault credential store/library
# and the Nomad↔Vault WIF role that workspaces in this project will use, and
# publishes the project's Nomad job templates to Vault KV.
#
# Connection + identity inputs (org scope id, jwt/kv mount paths, Nomad OIDC
# auth method) are NOT here — they are read from the foundation state (local.f,
# see main.tf). Only per-project facts + tunables live below.
#
# Isolation model: per-project Vault mount `ssh/<project>` (NOT a Vault
# Enterprise namespace), a scoped Boundary token, and a per-project WIF role on
# the SHARED `jwt-nomad` auth method created by the foundation tier.
# ---------------------------------------------------------------------------

variable "project_name" {
  description = <<-EOT
    Project identifier. Reused verbatim as the Boundary project-scope name, the
    Nomad namespace, the Vault SSH mount prefix (`ssh/<project>`) and the
    per-project WIF role name. Must be DNS/namespace-safe (lowercase, no slashes).
  EOT
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{0,61}[a-z0-9]$", var.project_name))
    error_message = "project_name must be lowercase alphanumeric/hyphen (a valid Nomad namespace + Vault path segment)."
  }
}

variable "developers_group_name" {
  description = <<-EOT
    IBM Verify group whose members are confined (defense-in-depth, IF ever handed
    Nomad SSO) to WRITE within THIS project's namespace only. The project team
    creates this group in IBM Verify; developers reach workspaces through Boundary,
    not Nomad, so this is a belt-and-suspenders Nomad ACL, not the primary control.
  EOT
  type        = string
}

variable "vault_cred_store_address" {
  description = <<-EOT
    Vault API address the Boundary CONTROLLER uses to reach Vault. The controller
    is co-located with Vault, so it talks locally (no NLB round-trip). Distinct
    from the vault provider's NLB address used by Terraform.
  EOT
  type        = string
  default     = "https://127.0.0.1:8200"
}

variable "boundary_token_period" {
  description = "Renewal period for the periodic Vault token the Boundary credential store uses (Boundary self-renews it)."
  type        = string
  default     = "24h"
}

variable "workspace_user" {
  description = "Unix user the signed SSH cert is valid for and that the cred library injects (matches the workspace image)."
  type        = string
  default     = "dev"
}

variable "cert_ttl" {
  description = "Default TTL of a signed SSH user cert (validated only at the SSH handshake)."
  type        = string
  default     = "5m"
}

variable "cert_max_ttl" {
  description = "Max TTL a caller may request for a signed SSH user cert."
  type        = string
  default     = "10m"
}

# --- Workspace templates (each template = its own repo + image + metadata) ---

variable "workspace_templates" {
  description = <<-EOT
    Map of job-template name => its per-template configuration. The key MUST match a
    `templates/<name>.nomad.hcl` file, and ONLY the templates listed here are published
    for this project (a project opts into the flavors it wants). Each template clones
    its OWN git repo and runs its OWN pinned Docker image — both project-owned (not
    developer-chosen) and baked into the published job template. `label` + `description`
    are surfaced to developers in the portal's template picker; `node_pool` targets a
    Nomad node pool (e.g. "gpu"), empty selects the default pool.

      workspace_templates = {
        "dev-workspace" = {
          image        = "you/dev-workspace:poc"
          git_repo_url = "https://github.com/acme/api-repo"
          label        = "Standard Dev Workspace"
          description  = "Full-stack API dev environment."
        }
      }

    The project team builds and pushes each image from terraform/project/images/<name>/.
  EOT
  type = map(object({
    image        = string
    git_repo_url = string
    label        = optional(string)
    description  = optional(string)
    node_pool    = optional(string, "")
  }))

  validation {
    condition     = alltrue([for t in values(var.workspace_templates) : can(regex("^https://", t.git_repo_url))])
    error_message = "each workspace_templates[*].git_repo_url must be an https:// URL (the workspace clones over HTTPS with the GitHub App token)."
  }
}

# --- LLM access ---
# The project no longer holds a provider API key. Claude Code routes through the
# shared LiteLLM AI gateway (terraform/infra/llm-gateway.tf) with a per-project
# virtual key minted in llm-gateway.tf; the DeepSeek key lives only on the gateway
# (set deepseek_api_key in the infra tier).

# --- GitHub App (the per-project git push credential broker) ---

variable "github_app_id" {
  description = "GitHub App ID for this project's bot (numeric). From the App's settings page."
  type        = number
}

variable "github_app_installation_id" {
  description = "Installation ID of the GitHub App on this project's account/org (numeric). From the installation URL …/installations/<id>."
  type        = number
}

variable "github_app_private_key" {
  description = <<-EOT
    GitHub App private key, PKCS#1 PEM (`-----BEGIN RSA PRIVATE KEY-----`). If
    GitHub issued PKCS#8 (`-----BEGIN PRIVATE KEY-----`), convert with
    `openssl rsa -in key.pem -out key.pkcs1.pem`. Stored only in Vault; supply
    via the gitignored <project>.tfvars (never commit).
  EOT
  type        = string
  sensitive   = true
}

variable "github_repositories" {
  description = <<-EOT
    Optional repo names to further constrain the minted token to (e.g.
    ["confused-deputy-aws"]). Empty (default) = the token covers all repos the
    App installation can access. Scoping is already bounded by which repos the
    App is installed on.
  EOT
  type        = list(string)
  default     = []
}

variable "enable_pki_example" {
  description = <<-EOT
    Mount an example PKI secrets engine (with a self-signed root CA + a server
    role) into this project's Vault namespace at pki/<project_name>. OFF by
    default. This is a PRECONDITION DEMO for the deploy-time path-grant feature:
    an MCP server (e.g. vault-mcp) can be granted access to it by a project-admin
    at deploy time. Any secrets engine works — this is just a concrete, applyable
    example of the operator's "mount the engine first" step.
  EOT
  type        = bool
  default     = false
}
