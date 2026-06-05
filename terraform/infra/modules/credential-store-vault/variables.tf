variable "project_scope_id" {
  description = "Boundary project scope ID where the Vault credential store is created"
  type        = string
}

variable "vault_cred_store_address" {
  description = <<-EOT
    Vault API address the Boundary CONTROLLER uses to reach Vault. The controller
    runs on the same node as Vault, so it talks to it locally (no NLB round-trip).
    This is distinct from the vault provider's NLB address used by Terraform.
  EOT
  type        = string
  default     = "https://127.0.0.1:8200"
}

variable "boundary_token_period" {
  description = "Renewal period for the periodic Vault token Boundary uses (Boundary self-renews it)"
  type        = string
  default     = "24h"
}
