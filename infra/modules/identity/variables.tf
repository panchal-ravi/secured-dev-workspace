variable "ibm_verify_tenant" {
  description = "IBM Verify SaaS tenant hostname (no scheme), e.g. myorg.verify.ibm.com"
  type        = string
}

variable "boundary_addr" {
  description = "Boundary controller API address (via the NLB), from the base module."
  type        = string
}

variable "nomad_addr" {
  description = "Nomad HTTP API address (via the NLB), from the base module."
  type        = string
}

variable "verify_access_token" {
  description = "IBM Verify bootstrap API bearer token, used to GET created apps back for their client credentials. Supplied by the root (same token that configures the restapi provider)."
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
