# ---------------------------------------------------------------------------
# Platform-tier Developer Portal. A single instance, deployed by the platform
# admin, that lets developers self-service workspaces (the direct-API equivalent
# of the terraform/workspace tier). Runs as a Nomad job in the "infra" namespace
# (defined in mcp-gateway.tf), terminating its own TLS on :8443 behind a new NLB
# listener locked to the operator /32.
#
# Credentials: the portal authenticates to Vault over WIF (no static token) and
# reads its config secrets — its OIDC client secret, a generated session key, and
# the privileged Boundary admin / Nomad mgmt creds it needs to provision — from
# Vault KV. The Verify OIDC app is created by the identity module (see
# modules/identity/verify.tf, gated on enable_developer_portal); its client
# id/secret flow in via module.identity outputs — no hand-registration, no
# portal_oidc_client_* vars.
# ---------------------------------------------------------------------------
# Variables for this feature are declared in variables.tf (Developer Portal group).

locals {
  developer_portal_port = 8443
  portal_count          = var.enable_developer_portal ? 1 : 0
  # The portal's Verify app redirect URI MUST be registered to match this exactly.
  portal_redirect_url = "https://${module.secured_codespace.nlb_dns_name}:${local.developer_portal_port}/auth/callback"
}

# Cookie signing key — generated per deploy, never a known default.
resource "random_password" "portal_session_secret" {
  count   = local.portal_count
  length  = 48
  special = false
}

# Self-signed cert for the browser-facing :8443 listener. The NLB is TCP
# pass-through, so the portal must terminate TLS itself for Secure cookies to be
# honored. /32-locked; a CA-signed cert (Vault PKI) is the hardening step.
resource "tls_private_key" "portal" {
  count     = local.portal_count
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "portal" {
  count           = local.portal_count
  private_key_pem = tls_private_key.portal[0].private_key_pem
  dns_names       = [module.secured_codespace.nlb_dns_name]

  subject {
    common_name  = module.secured_codespace.nlb_dns_name
    organization = "secured-dev-workspace"
  }

  validity_period_hours = 8760 # 1 year
  allowed_uses          = ["key_encipherment", "digital_signature", "server_auth"]
}

# Portal config secrets in the shared KV v2 mount, under infra/. Read by the job
# over WIF. Boundary admin creds and the Nomad mgmt token come straight from the
# secured-codespace module outputs (no second copy of the credential).
resource "vault_kv_secret_v2" "developer_portal" {
  count = local.portal_count
  mount = vault_mount.kv.path
  name  = "infra/developer-portal"
  # The portal-postgres password is copied here (when the admin plane is on) so the
  # portal can assemble PORTAL_DB_DSN in its jobspec over its existing WIF read of
  # this path — no second WIF role for the portal.
  data_json = jsonencode(merge({
    oidc_client_secret = module.identity.portal_app_client_secret
    session_secret     = random_password.portal_session_secret[0].result
    boundary_login     = module.secured_codespace.admin_login_name
    boundary_password  = module.secured_codespace.admin_password
    nomad_token        = module.secured_codespace.nomad_management_token
    tls_cert           = tls_self_signed_cert.portal[0].cert_pem
    tls_key            = tls_private_key.portal[0].private_key_pem
    }, var.enable_platform_admin ? {
    pg_password = random_password.portal_pg[0].result
  } : {}))
}

# WIF read policy + role: the portal job may read its own secret path and the
# project descriptors/job-templates it provisions from — nothing else.
resource "vault_policy" "infra_portal_read" {
  count = local.portal_count
  name  = "infra-developer-portal-read"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/developer-portal" {
      capabilities = ["read"]
    }
    path "${vault_mount.kv.path}/data/projects/*" {
      capabilities = ["read"]
    }
    path "${vault_mount.kv.path}/metadata/projects" {
      capabilities = ["list"]
    }
    path "${vault_mount.kv.path}/metadata/projects/*" {
      capabilities = ["list", "read"]
    }
  HCL
}

# Blueprint-provisioning grant for the project-MCP deploy plane (B2 / R3). Attached
# to the portal WIF role below; applies INSIDE whichever project namespace the
# portal's blueprint Executor targets via client.WithNamespace(<project>). See
# portal-provisioning-policy.hcl for the (honest) containment rationale. Gated on the
# onboarding plane, exactly like infra_platform_admin — the project deploy plane only
# initializes when the admin plane is enabled.
resource "vault_policy" "portal_provisioning" {
  count  = local.platform_admin_count
  name   = "portal-blueprint-provisioning"
  policy = file("${path.module}/portal-provisioning-policy.hcl")
}

resource "vault_jwt_auth_backend_role" "infra_portal" {
  count                   = local.portal_count
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-developer-portal"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies = concat(
    [vault_policy.infra_portal_read[0].name],
    var.enable_platform_admin ? [
      vault_policy.infra_platform_admin[0].name,
      vault_policy.portal_provisioning[0].name,
    ] : [],
  )
  token_ttl     = 1800
  token_max_ttl = 3600
  token_type    = "service"
}

# detach=false waits for the alloc to become healthy on apply. The portal reaches
# Vault/Nomad/Boundary over loopback on the all-in-one node.
resource "nomad_job" "developer_portal" {
  count            = local.portal_count
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/developer-portal.nomad.hcl.tftpl", {
    namespace               = nomad_namespace.infra.name
    image                   = var.developer_portal_image
    port                    = local.developer_portal_port
    wif_role                = vault_jwt_auth_backend_role.infra_portal[0].role_name
    kv_path                 = "${vault_mount.kv.path}/data/infra/developer-portal"
    kv_mount                = vault_mount.kv.path
    oidc_issuer             = var.portal_oidc_issuer
    oidc_client_id          = module.identity.portal_app_client_id
    oidc_redirect_url       = local.portal_redirect_url
    boundary_addr           = "https://127.0.0.1:9200"               # server-side API client over loopback (on-node)
    boundary_public_addr    = module.secured_codespace.boundary_addr # NLB addr for the developer-facing authenticate/connect commands
    boundary_auth_method_id = module.secured_codespace.admin_auth_method_id
    nomad_addr              = "https://127.0.0.1:4646"
    vault_addr              = "https://127.0.0.1:8200"

    # Platform Admin onboarding plane. When disabled, the gateway service-discovery
    # template + admin env are omitted entirely, so the portal starts the plane off.
    enable_platform_admin = var.enable_platform_admin
    # Gateway addresses are resolved at runtime via Nomad service discovery in the
    # jobspec (nomadService "mcp-gateway"/"llm-gateway"), not injected here.
    mcp_namespace   = var.enable_platform_admin ? nomad_namespace.infra_mcp[0].name : ""
    agent_node_pool = var.platform_admin_mcp_node_pool
    # Postgres reachable on the all-in-one node (host-network); the portal builds
    # PORTAL_DB_DSN from this + the pg_password it reads over WIF. Unused when the
    # admin plane is off (the jobspec omits the DSN line entirely).
    db_host = "${module.secured_codespace.instance_private_ip}:${local.portal_pg_port}"
  })

  depends_on = [
    vault_kv_secret_v2.developer_portal,
    nomad_job.portal_postgres,
  ]
}
