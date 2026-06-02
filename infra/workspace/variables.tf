# ---------------------------------------------------------------------------
# Connection inputs (populate from `terraform -chdir=../infra output`).
# ---------------------------------------------------------------------------

variable "nomad_addr" {
  description = "Nomad API address behind the NLB (terraform output -raw nomad_addr)"
  type        = string
}

variable "nomad_token_path" {
  description = "Path to the Nomad ACL bootstrap output written by the base apply"
  type        = string
  default     = "../generated/nomad-setup.json"
}

variable "boundary_addr" {
  description = "Boundary controller API address behind the NLB"
  type        = string
}

variable "boundary_admin_auth_method_id" {
  description = "Boundary PASSWORD auth-method id — the provider authenticates as admin to create resources (terraform output -raw admin_auth_method_id)"
  type        = string
}

variable "boundary_admin_login" {
  description = "Boundary admin login name (terraform output -raw admin_login_name)"
  type        = string
}

variable "boundary_admin_password" {
  description = "Boundary admin password (terraform output -raw admin_password)"
  type        = string
  sensitive   = true
}

variable "boundary_oidc_auth_method_id" {
  description = "Boundary IBM Verify OIDC auth-method id — per-developer managed groups attach here (terraform output -raw boundary_oidc_auth_method_id)"
  type        = string
}

variable "boundary_project_scope_id" {
  description = "Boundary project scope id where workspace targets/roles live (terraform output -raw project_scope_id)"
  type        = string
}

variable "workspace_host_address" {
  description = <<-EOT
    Address the co-located Boundary worker dials to reach the workspace SSH host ports.
    Nomad's docker driver publishes static ports on the node's PRIMARY private IP (not
    127.0.0.1/0.0.0.0), so this must be the node's private IP — find it with:
      ssh <node> 'hostname -I' or `aws_instance.this.private_ip`.
    NOTE: still off the NLB — only the on-node worker reaches this address.
  EOT
  type        = string
}

# ---------------------------------------------------------------------------
# The PoC's projects and developers. One example project is enough; the shapes
# scale to N developers x M workspaces with no code change — which is exactly
# what the future portal will drive programmatically.
# ---------------------------------------------------------------------------

variable "projects" {
  description = "Projects, each mapped to a Nomad namespace. Key = project/namespace name."
  type = map(object({
    git_repo_url = string # PUBLIC repo, cloned over HTTPS at bootstrap (no creds — PoC)
    image        = string # PUBLIC Docker Hub path, e.g. "youruser/dev-workspace:poc" — Nomad pulls it
  }))
}

variable "developers" {
  description = "Developers and their workspaces. Key = developer handle."
  type = map(object({
    email = string            # IBM Verify OIDC /token/email claim — the managed-group filter AND the cert key_id
    workspaces = map(object({ # key = workspace name
      project  = string       # must be a key in var.projects (= the namespace)
      ssh_port = number       # DISTINCT static host port per workspace (shared node)
    }))
  }))
}

variable "vault_credential_store_id" {
  description = "Boundary Vault credential store id in the project scope (terraform -chdir=../infra output -raw vault_credential_store_id)"
  type        = string
}

variable "vault_ssh_sign_path" {
  description = "Vault path Boundary signs SSH certs against (terraform -chdir=../infra output -raw ssh_sign_path)"
  type        = string
  default     = "ssh-client-signer/sign/dev-workspace"
}

variable "workspace_session_max_seconds" {
  description = <<-EOT
    Boundary target max session duration (seconds). The signed cert TTL is short
    (5m) but is validated only at the SSH handshake; THIS governs how long a
    connected session stays up. Default 8h.
  EOT
  type        = number
  default     = 28800
}

variable "alias_suffix" {
  description = <<-EOT
    DNS-like suffix for Boundary target aliases (the value the Client Agent
    intercepts), e.g. "boundary" yields main.ravi.project-acme.boundary. A
    dedicated suffix keeps the alias namespace clear of real DNS the laptop
    resolves. Aliases live in the global scope.
  EOT
  type        = string
  default     = "boundary"
}
