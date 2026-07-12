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

# --- Developer Portal OIDC app (created only when the portal is deployed) ---

variable "create_portal_app" {
  description = "Create the Developer Portal's IBM Verify OIDC app. Gated on enable_developer_portal in the root; counts against the tenant's 5-app limit."
  type        = bool
  default     = false
}

variable "portal_redirect_url" {
  description = "The portal's OIDC redirect URI (https://<nlb>:8443/auth/callback), derived in the root from the live NLB. Required when create_portal_app = true."
  type        = string
  default     = ""
}

variable "portal_audiences" {
  description = "Access-token audiences for the portal app (e.g. the token-exchange client id the RFC 8693 OBO flow targets)."
  type        = list(string)
  default     = []
}
