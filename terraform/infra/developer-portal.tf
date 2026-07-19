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
  # The project-create plane rides on the platform-admin plane (both are portal-admin
  # capabilities): a scoped-ephemeral Vault creator role, a dedicated Nomad token, and
  # a scoped Boundary account (see boundary-portal.tf).
  project_creator_count = var.enable_developer_portal && var.enable_platform_admin ? 1 : 0
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
  # When the project-create plane is on, the portal uses tightened creds: a
  # dedicated (revocable) Nomad management token and a scoped Boundary account
  # (org-subtree admin, not the global bootstrap admin). Otherwise it falls back to
  # the bootstrap admin creds it used before.
  data_json = jsonencode(merge({
    oidc_client_secret = module.identity.portal_app_client_secret
    session_secret     = random_password.portal_session_secret[0].result
    boundary_login     = local.project_creator_count > 0 ? boundary_account_password.portal[0].login_name : module.secured_codespace.admin_login_name
    boundary_password  = local.project_creator_count > 0 ? random_password.portal_boundary[0].result : module.secured_codespace.admin_password
    nomad_token        = local.project_creator_count > 0 ? nomad_acl_token.portal[0].secret_id : module.secured_codespace.nomad_management_token
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
    ] : [],
  )
  token_ttl     = 1800
  token_max_ttl = 3600
  token_type    = "service"
}

# ---------------------------------------------------------------------------
# Project-create plane. A third portal workload identity (aud=vault-creator) is
# exchanged at the ROOT-namespace jwt-nomad backend for a SHORT-TTL token that can
# create a child namespace and bootstrap its auth (enable jwt-nomad, mount the
# project KV, seed the portal-provisioner role/policy + workspace WIF role) — then
# it is discarded. The §5 provisioner plane takes over for in-namespace engine
# work. No standing cross-namespace privilege. Driver: portal/backend/internal/
# projectbootstrap.
#
# KNOWN LIVE-VALIDATION ITEM: the "+/…" single-segment globs must match a
# child-namespace path (e.g. project-acme/sys/auth/jwt-nomad) for a root-minted
# token operating cross-namespace — the §5 ACL seam in reverse. Confirm against
# Vault Enterprise; if the glob does not match, widen "+/…" paths to "+/*" (same
# denies).
resource "vault_policy" "project_creator" {
  count = local.project_creator_count
  name  = "project-creator"

  policy = <<-HCL
    # Create + inspect + delete child namespaces (root namespace op). Delete backs
    # the portal's platform-admin "delete project" — Vault queues removal of the
    # namespace's full contents.
    path "sys/namespaces/*" {
      capabilities = ["create", "read", "update", "delete"]
    }
    # Bootstrap a freshly-created child namespace (namespace-prefixed globs). Enabling
    # an auth method requires sudo on the child's sys/auth path.
    path "+/sys/mounts/*" {
      capabilities = ["create", "read", "update", "delete"]
    }
    path "+/sys/auth/*" {
      capabilities = ["create", "read", "update", "delete", "sudo"]
    }
    path "+/auth/jwt-nomad/*" {
      capabilities = ["create", "read", "update", "delete"]
    }
    path "+/sys/policies/acl" {
      capabilities = ["list"]
    }
    path "+/sys/policies/acl/*" {
      capabilities = ["create", "read", "update", "delete", "list"]
    }
    # Descriptor + job-templates in the shared ROOT KV.
    path "${vault_mount.kv.path}/data/projects/*" {
      capabilities = ["create", "read", "update"]
    }
    path "${vault_mount.kv.path}/metadata/projects/*" {
      capabilities = ["read", "list"]
    }
    # Denies win — no identity engine, no cross-namespace secret exfiltration.
    path "identity/*" { capabilities = ["deny"] }
    path "+/identity/*" { capabilities = ["deny"] }
    path "cubbyhole/*" { capabilities = ["deny"] }
  HCL
}

resource "vault_jwt_auth_backend_role" "project_creator" {
  count                   = local.project_creator_count
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "project-creator"
  role_type               = "jwt"
  bound_audiences         = ["vault-creator"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  bound_claims = {
    nomad_namespace = "infra"
    nomad_job_id    = "developer-portal"
  }
  token_policies = [vault_policy.project_creator[0].name]
  token_ttl      = 120
  token_type     = "service"
}

# Dedicated, revocable Nomad management token for the portal — replaces the shared
# bootstrap management token in the KV secret above. Nomad namespace/ACL/binding-rule
# administration is management-only (no capability-scoped equivalent), so this is a
# revocability/auditability tightening, not a true privilege scope-down.
resource "nomad_acl_token" "portal" {
  count = local.project_creator_count
  name  = "developer-portal"
  type  = "management"
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
    agent_node_pool = var.platform_admin_mcp_node_pool
    # Postgres reachable on the all-in-one node (host-network); the portal builds
    # PORTAL_DB_DSN from this + the pg_password it reads over WIF. Unused when the
    # admin plane is off (the jobspec omits the DSN line entirely).
    db_host = "${module.secured_codespace.instance_private_ip}:${local.portal_pg_port}"

    # Project-create plane. The creator identity + env render only when the plane is
    # on; the coordinates below are baked into each new project's descriptor + child
    # namespace auth. nomad_ca_pem (multi-line) is written to a file, not env.
    enable_project_creator  = local.project_creator_count > 0
    nomad_jwks_url          = "https://127.0.0.1:4646/.well-known/jwks.json"
    nomad_ca_pem            = module.secured_codespace.nomad_ca_pem
    nomad_oidc_auth_method  = module.identity.nomad_oidc_auth_method_name
    boundary_org_scope_id   = module.secured_codespace.org_scope_id
    boundary_oidc_method_id = module.identity.boundary_oidc_auth_method_id
    instance_private_ip     = module.secured_codespace.instance_private_ip

    # Governed LLM models (single source: local.llm_model_* in llm-gateway.tf). The
    # portal bakes these into project templates (Claude Code model slots) + uses the
    # list as the allowed set on each project's LiteLLM virtual key.
    llm_models        = join(",", local.llm_model_names)
    llm_model_primary = local.llm_model_primary
    llm_model_fast    = local.llm_model_fast

    # Per-project shared volumes (EFS). When enabled, the portal dynamically
    # provisions one EFS access point per named shared volume against this
    # filesystem via the Nomad CSI API. Empty id when the feature is off.
    shared_volume_enabled    = var.enable_shared_volume
    shared_efs_filesystem_id = module.secured_codespace.efs_filesystem_id
  })

  depends_on = [
    vault_kv_secret_v2.developer_portal,
    nomad_job.portal_postgres,
  ]
}
