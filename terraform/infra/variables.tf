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
