# ---------------------------------------------------------------------------
# Nomad→Boundary host-sync. An external reconciler that keeps each workspace's
# Boundary host address pointed at whatever Nomad node its alloc currently runs on,
# so the (stable) per-workspace Boundary target follows the workspace across
# reschedules — the reachability half of durable workspaces (EBS-CSI is the state
# half). Boundary Enterprise cannot embed a custom dynamic-host plugin, so this runs
# the same idea OUTSIDE Boundary, driving its API.
#
# Single stateless instance in the "infra" namespace on the all-in-one node (host
# network), reaching Boundary/Nomad over loopback. Boundary admin creds + a read-only
# Nomad token come from Vault KV over WIF. Driver: /nomad-boundary-host-sync.
# ---------------------------------------------------------------------------
locals {
  host_sync_count = var.enable_developer_portal ? 1 : 0
}

# Read-only Nomad token: list workspace service registrations across all namespaces.
# The sync only reads the Nomad service catalog (it takes the node address straight
# from each registration), so no write/node capability is needed.
resource "nomad_acl_policy" "host_sync" {
  count       = local.host_sync_count
  name        = "nomad-boundary-host-sync-read"
  description = "Read-only Nomad service discovery for the Boundary host-sync."

  rules_hcl = <<-HCL
    namespace "*" {
      capabilities = ["list-jobs", "read-job"]
    }
  HCL
}

resource "nomad_acl_token" "host_sync" {
  count    = local.host_sync_count
  name     = "nomad-boundary-host-sync"
  type     = "client"
  policies = [nomad_acl_policy.host_sync[0].name]
}

# Config secrets in the shared KV, read by the job over WIF. The Boundary account is
# the SAME one the portal uses (scoped org-subtree admin when the project-create plane
# is on, else the bootstrap admin) — host CRUD needs the same Boundary reach as the
# portal's per-workspace provisioning.
resource "vault_kv_secret_v2" "nomad_boundary_host_sync" {
  count = local.host_sync_count
  mount = vault_mount.kv.path
  name  = "infra/nomad-boundary-host-sync"
  data_json = jsonencode({
    boundary_login    = local.project_creator_count > 0 ? boundary_account_password.portal[0].login_name : module.secured_codespace.admin_login_name
    boundary_password = local.project_creator_count > 0 ? random_password.portal_boundary[0].result : module.secured_codespace.admin_password
    nomad_token       = nomad_acl_token.host_sync[0].secret_id
  })
}

# WIF read policy + role: the sync job may read only its own secret path.
resource "vault_policy" "infra_host_sync_read" {
  count = local.host_sync_count
  name  = "infra-nomad-boundary-host-sync-read"

  policy = <<-HCL
    path "${vault_mount.kv.path}/data/infra/nomad-boundary-host-sync" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_host_sync" {
  count                   = local.host_sync_count
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-nomad-boundary-host-sync"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  bound_claims = {
    nomad_namespace = "infra"
    nomad_job_id    = "nomad-boundary-host-sync"
  }
  token_policies = [vault_policy.infra_host_sync_read[0].name]
  token_ttl      = 1800
  token_max_ttl  = 3600
  token_type     = "service"
}

# detach=false waits for the alloc to become healthy on apply. The sync reaches
# Nomad/Boundary over loopback on the all-in-one node.
resource "nomad_job" "nomad_boundary_host_sync" {
  count            = local.host_sync_count
  detach           = false
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/templates/nomad-boundary-host-sync.nomad.hcl.tftpl", {
    namespace               = nomad_namespace.infra.name
    image                   = var.nomad_boundary_host_sync_image
    wif_role                = vault_jwt_auth_backend_role.infra_host_sync[0].role_name
    kv_path                 = "${vault_mount.kv.path}/data/infra/nomad-boundary-host-sync"
    nomad_addr              = "https://127.0.0.1:4646"
    boundary_addr           = "https://127.0.0.1:9200"
    boundary_auth_method_id = module.secured_codespace.admin_auth_method_id
    boundary_org_scope_id   = module.secured_codespace.org_scope_id
  })

  depends_on = [
    vault_kv_secret_v2.nomad_boundary_host_sync,
    nomad_job.developer_portal,
  ]
}
