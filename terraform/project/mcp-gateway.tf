# ---------------------------------------------------------------------------
# Per-project virtual MCP server in the shared ContextForge gateway. For each
# project the project-admin: (1) registers the project's demo-db-mcp as a peer in
# the platform gateway, (2) composes a per-project "virtual server" bundling its
# tools, (3) creates a per-project client token scoped to that virtual server, and
# (4) writes the virtual server URL + token to Vault KV (secret/projects/<project>/
# mcp). The workspace reads that over WIF and points Claude's remote MCP at it. The
# gateway itself lives in the platform tier (terraform/infra/mcp-gateway.tf).
#
# The register→discover→compose flow is dynamic (tool ids are only known after the
# peer is registered), so it runs in scripts/mcp-provision.sh via a terraform_data
# create provisioner; a destroy-time provisioner tears the virtual server + peer +
# KV path back down. Mirrors the local-exec pattern in
# infra/modules/identity/verify.tf. All inputs (incl. secrets — already in this
# project's state via the data source below and demo-db's admin password) are
# passed via the ENVIRONMENT, never the command line.
# ---------------------------------------------------------------------------

locals {
  mcp_peer_name             = "demo-db-${var.project_name}"
  mcp_virtual_server        = "demo-db-${var.project_name}"
  mcp_client_username       = "${var.project_name}-mcp"
  mcp_client_token_exp_days = 365 # PoC; rotate by re-apply
  mcp_kv_name               = "projects/${var.project_name}/mcp"
  mcp_peer_url              = "http://${local.f.instance_private_ip}:${local.demo_db_mcp_port}/sse"
}

# The gateway's signing secret + admin email (written by the infra tier). Used to
# mint the admin JWT for the /gateways, /servers and /tokens admin calls (the
# per-project client token is created via POST /tokens, not minted from this).
data "vault_kv_secret_v2" "mcp_gateway" {
  mount = local.f.kv_mount_path
  name  = "infra/mcp-gateway"
}

resource "terraform_data" "mcp_provision" {
  # Re-run when the peer endpoint or the virtual-server name changes. The
  # token_mechanism marker forces a replace when the client-credential mechanism
  # changes (hand-minted JWT -> server-scoped gateway API token).
  triggers_replace = jsonencode({
    peer_url        = local.mcp_peer_url
    vs_name         = local.mcp_virtual_server
    token_mechanism = "scoped-api-token"
  })

  # Stashed in state so the destroy-time provisioner (which cannot read data
  # sources/locals) still has everything it needs. This project's state is already
  # secret-bearing and gitignored.
  input = {
    GW_URL          = local.f.mcp_gateway_addr
    GW_PRIVATE_URL  = local.f.mcp_gateway_private_endpoint
    PROJECT         = var.project_name
    PEER_NAME       = local.mcp_peer_name
    PEER_URL        = local.mcp_peer_url
    VS_NAME         = local.mcp_virtual_server
    ADMIN_EMAIL     = data.vault_kv_secret_v2.mcp_gateway.data["admin_email"]
    JWT_SECRET      = data.vault_kv_secret_v2.mcp_gateway.data["jwt_secret_key"]
    CLIENT_USERNAME = local.mcp_client_username
    CLIENT_EXP_DAYS = tostring(local.mcp_client_token_exp_days)
    VAULT_ADDR      = local.f.vault_addr
    VAULT_TOKEN     = local.f.vault_root_token
    KV_MOUNT        = local.f.kv_mount_path
    KV_NAME         = local.mcp_kv_name
  }

  provisioner "local-exec" {
    when        = create
    command     = "bash ${path.module}/scripts/mcp-provision.sh provision"
    environment = self.input
  }

  provisioner "local-exec" {
    when        = destroy
    command     = "bash ${path.module}/scripts/mcp-provision.sh deprovision"
    environment = self.input
  }

  # The peer must be reachable for tool discovery before we register it.
  depends_on = [nomad_job.demo_db_mcp]
}
