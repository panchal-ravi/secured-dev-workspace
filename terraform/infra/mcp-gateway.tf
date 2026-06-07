# ---------------------------------------------------------------------------
# Platform-tier ContextForge MCP Gateway (IBM mcp-context-forge). A single SHARED
# gateway, deployed ONCE by the platform admin, that federates per-project MCP
# servers and exposes each project a "virtual MCP server". Runs as a Nomad job in
# the dedicated "infra" namespace. Its secrets (JWT signing key, encryption
# secret, Admin-UI creds) are generated here, stored in Vault KV, and read by the
# job over WIF at start.
#
# Auth model (per IBM docs): HTTP Basic auth is left DISABLED — all API access is
# JWT bearer (HS256 over JWT_SECRET_KEY). PLATFORM_ADMIN_EMAIL/PASSWORD are for the
# Admin UI only. The project tier reads jwt_secret_key (below) to mint the admin
# JWT for its /gateways + /servers calls and the per-project client JWT the
# workspace presents.
# ---------------------------------------------------------------------------

locals {
  mcp_admin_email  = "admin@example.com" # Admin UI login only (no API authorization)
  mcp_gateway_port = 4444
}

# Platform-tier namespace for long-running shared services (the gateway today).
resource "nomad_namespace" "infra" {
  name        = "infra"
  description = "Platform-tier long-running services (MCP gateway, …)."
}

# Gateway secrets. 48-char alphanumerics (no special) so they are safe in an env
# file with no escaping; JWT_SECRET_KEY needs >= 32 chars.
resource "random_password" "mcp_jwt_secret" {
  length  = 48
  special = false
}

resource "random_password" "mcp_enc_secret" {
  length  = 48
  special = false
}

resource "random_password" "mcp_admin_pass" {
  length  = 24
  special = false
}

# Stored in the same KV v2 mount the project job templates use, under an infra/
# prefix. The gateway job reads this over WIF; the project tier reads jwt_secret_key
# from here (via its root vault provider) to mint admin + per-project client JWTs.
resource "vault_kv_secret_v2" "mcp_gateway" {
  mount = vault_mount.kv.path
  name  = "infra/mcp-gateway"
  data_json = jsonencode({
    jwt_secret_key = random_password.mcp_jwt_secret.result # signs ALL tokens (admin + client)
    enc_secret     = random_password.mcp_enc_secret.result
    admin_email    = local.mcp_admin_email
    admin_password = random_password.mcp_admin_pass.result
  })
}

# WIF read policy + role: the gateway job (Nomad workload identity) may read ONLY
# its own secret path. Attaches to the shared jwt-nomad backend, same as the
# per-project roles; bound_audiences must match the agent default_identity.aud.
resource "vault_policy" "infra_mcp_read" {
  name = "infra-mcp-gateway-read"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/mcp-gateway" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_mcp" {
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-mcp-gateway"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.infra_mcp_read.name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
}

# Persistent backing store for the gateway's SQLite registry (registered peers +
# virtual servers survive restarts). Dynamic host volume on the default pool — the
# gateway runs on the all-in-one node. Same mechanism as the workspace /home/dev.
# NOTE: lives on the node root volume — survives stop/start + reboot, NOT an
# instance replacement (durable storage is the roadmap fix).
resource "nomad_dynamic_host_volume" "mcp_gateway" {
  name      = "mcp-gateway-data"
  namespace = nomad_namespace.infra.name
  plugin_id = "mkdir"
  node_pool = "default"

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }
}

# NOTE: detach=false waits for the alloc to become healthy on apply. The job reads
# its secrets over WIF and serves plaintext HTTP on :4444 (the gateway terminates
# no TLS — PoC; the NLB listener is locked to the operator /32 and TLS is a
# hardening step). The mcp-context-forge image runs as a non-root user (uid 10001)
# and cannot write the SQLite file on the root-owned mkdir host volume, so a
# prestart task in the jobspec chowns the volume to that uid before the gateway
# starts (without it the gateway's db_isready probe fails and startup aborts).
resource "nomad_job" "mcp_gateway" {
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/mcp-gateway.nomad.hcl.tftpl", {
    namespace = nomad_namespace.infra.name
    image     = var.mcp_gateway_image
    port      = local.mcp_gateway_port
    volume    = nomad_dynamic_host_volume.mcp_gateway.name
    wif_role  = vault_jwt_auth_backend_role.infra_mcp.role_name
    kv_path   = "${vault_mount.kv.path}/data/infra/mcp-gateway"
    # SSRF allowlist: the gateway blocks RFC-1918 peer URLs by default. Per-project
    # MCP peers run on the node-private network, so allow the VPC CIDR (and only
    # that — cloud-metadata endpoints stay blocked, private nets stay off by default).
    ssrf_allowed_networks = module.secured_codespace.vpc_cidr
  })

  depends_on = [nomad_dynamic_host_volume.mcp_gateway, vault_kv_secret_v2.mcp_gateway]
}
