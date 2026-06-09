# ---------------------------------------------------------------------------
# Developer-tier root (flat — no child module). Provisions ONE developer
# workspace: the Nomad job + persistent host volume (here) and the Boundary
# target/alias/managed-group graph (boundary.tf). One Terraform workspace per
# developer-workspace (`terraform workspace new alice-main`).
#
# All connection + wiring inputs are read from the FOUNDATION (local.f) and
# PROJECT (local.p) states via terraform_remote_state — no token/address
# copying. The project state lives under the project root's per-project
# Terraform workspace (named to match project_name), so the path interpolates
# var.project_name. Apply order: foundation -> project (for this project) ->
# here. Vault must be UNSEALED (this reads the KV job template; Boundary signs
# the session cert per connect).
# ---------------------------------------------------------------------------
data "terraform_remote_state" "foundation" {
  backend = "local"
  config = {
    path = "${path.module}/../infra/terraform.tfstate"
  }
}

data "terraform_remote_state" "project" {
  backend = "local"
  config = {
    path = "${path.module}/../project/terraform.tfstate.d/${var.project_name}/terraform.tfstate"
  }
}

locals {
  f = data.terraform_remote_state.foundation.outputs
  p = data.terraform_remote_state.project.outputs

  # alice/main -> alice-main: the DNS/Nomad-safe id reused for every resource name.
  slug        = "${var.developer_handle}-${var.workspace_name}"
  job_name    = "ws-${local.slug}"
  volume_name = "home-${local.slug}"

  # Node pool of the selected flavor, pinned per template by the project tier
  # (workspace_templates[*].node_pool, surfaced as a project output). "" = the
  # implicit default pool (main node); "gpu" places the workspace on the GPU
  # worker. try() keeps default-pool workspaces working against a project state
  # applied before this output existed — only GPU flavors require the project
  # re-apply. Mirrors the portal's node-pool-aware provisioning.
  node_pool = try(local.p.job_template_node_pools[var.job_template_name], "")

  # The node Boundary's worker dials for this workspace. One node per pool on this
  # single-node PoC, so the address is static per pool: default pool -> main node,
  # "gpu" -> the GPU worker. (The portal instead resolves it at runtime from the
  # placed allocation's node attribute; here the mapping is known up front.)
  # try() because the foundation drops gpu_instance_private_ip from its outputs
  # when enable_gpu_node = false (Terraform omits null-valued outputs from state) —
  # default-pool workspaces must keep resolving even with no GPU node provisioned.
  host_address = local.node_pool == "gpu" ? try(local.f.gpu_instance_private_ip, null) : local.f.instance_private_ip
}

# The project tier published this project's job templates (raw HCL) to Vault KV at
# onboarding. Read the selected one back to render it for this workspace.
data "vault_kv_secret_v2" "job_template" {
  mount = local.f.kv_mount_path
  name  = "projects/${var.project_name}/job-templates/${var.job_template_name}"
}

# Persistent /home/dev. Dynamic host volumes (Nomad 1.10+) survive stop/start +
# reboot WITHOUT a static client-config host_volume (which would need a nomad.hcl
# change + instance replace). The built-in `mkdir` plugin creates the backing dir;
# the job entrypoint chowns it to the dev user. The namespace already exists (the
# project tier created it) — this root only deploys into it.
# NOTE: lives on the node root volume — survives stop/start + reboot, NOT an
# instance replacement (CSI/EBS durable storage is the roadmap fix).
resource "nomad_dynamic_host_volume" "home" {
  name      = local.volume_name
  namespace = local.p.namespace
  plugin_id = "mkdir"

  # Pin the volume to the flavor's node pool so it materializes on a node the job
  # can actually be placed on. The default pool is pinned explicitly too: otherwise
  # the scheduler may put the volume on the GPU node, where a default-pool job can
  # never co-locate with it ("missing compatible host volumes").
  node_pool = local.node_pool != "" ? local.node_pool : "default"

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }
}

# The workspace job. The project tier already baked every project-static value
# (namespace, image, git_repo_url, wif_role, ssh_ca_path, github/mcp/llm paths)
# into the published jobspec at onboarding (kv.tf), leaving only the per-workspace
# placeholders as literal `${...}`. We fill exactly those five — the same set the
# Developer Portal renders — so the workspace tier and the portal produce an
# identical job from the identical template.
resource "nomad_job" "workspace" {
  detach           = false # wait for the deployment to become healthy on apply
  purge_on_destroy = true

  jobspec = templatestring(data.vault_kv_secret_v2.job_template.data["jobspec"], {
    job_name        = local.job_name
    ssh_port        = var.ssh_port
    volume_name     = nomad_dynamic_host_volume.home.name
    developer_email = var.developer_email
    git_user_name   = var.developer_handle
  })

  depends_on = [nomad_dynamic_host_volume.home]
}
