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

variable "boundary_version" {
  description = "Boundary Enterprise version baked into the AMI (informational)"
  type        = string
  default     = "0.21.3+ent"
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

variable "boundary_project_name" {
  description = "Name of the project scope created under the org"
  type        = string
  default     = "primary-project"
}
