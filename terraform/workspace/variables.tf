# ---------------------------------------------------------------------------
# Per-workspace inputs. Connection details + project wiring are NOT here — they
# are read from the foundation and project states (see providers.tf). Use one
# Terraform workspace per developer-workspace, e.g.:
#   terraform workspace new alice-main
#   terraform apply -var-file=alice-main.tfvars
# ---------------------------------------------------------------------------

variable "developer_email" {
  description = "IBM Verify /token/email claim — managed-group filter AND injected cert key_id."
  type        = string
}

variable "developer_handle" {
  description = "Short developer handle (e.g. alice) — used in resource names and the alias."
  type        = string
}

variable "workspace_name" {
  description = "Workspace name (e.g. main)."
  type        = string
  default     = "main"
}

variable "project_name" {
  description = <<-EOT
    Project this workspace belongs to. MUST match an applied project (the project
    root's Terraform workspace of the same name); its state is read for the scope,
    namespace, credential library, WIF role and SSH CA path.
  EOT
  type        = string
}

variable "ssh_port" {
  description = "DISTINCT static SSH host port for this workspace on the shared node."
  type        = number
}

variable "git_repo_url" {
  description = "github.com repo cloned into /home/dev on first boot (HTTPS). Private repos work — the Vault-minted GitHub App token is supplied by the in-container credential helper."
  type        = string
}

variable "job_template_name" {
  description = "Which of the project's Vault-KV job templates (a 'flavor') to render. The image is pinned to the template by the project tier, so picking the flavor picks the image."
  type        = string
  default     = "dev-workspace"
}

variable "workspace_user" {
  description = "Login user inside the container (matches the project signing role's default user)."
  type        = string
  default     = "dev"
}

variable "workspace_session_max_seconds" {
  description = "Boundary target max session duration (seconds). Default 8h."
  type        = number
  default     = 28800
}

variable "alias_suffix" {
  description = "DNS-like suffix for the Boundary target alias (e.g. boundary => main.alice.project-acme.boundary)."
  type        = string
  default     = "boundary"
}
