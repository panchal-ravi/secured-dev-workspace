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

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }
}

# The workspace job. The KV blob carries consul-template `{{ }}` + bash untouched;
# templatestring() fills only the Terraform `${...}` placeholders — including the
# per-project wif_role and ssh_ca_path, which is why the template is shared across
# projects but rendered per workspace.
resource "nomad_job" "workspace" {
  detach           = false # wait for the deployment to become healthy on apply
  purge_on_destroy = true

  jobspec = templatestring(data.vault_kv_secret_v2.job_template.data["jobspec"], {
    job_name          = local.job_name
    namespace         = local.p.namespace
    image             = data.vault_kv_secret_v2.job_template.data["image"]
    ssh_port          = var.ssh_port
    volume_name       = nomad_dynamic_host_volume.home.name
    git_repo_url      = var.git_repo_url
    wif_role          = local.p.wif_role
    ssh_ca_path       = local.p.ssh_ca_path
    developer_email   = var.developer_email
    git_user_name     = var.developer_handle
    github_token_path = local.p.github_token_path
    db_creds_path     = local.p.db_creds_path
    db_endpoint       = local.p.db_endpoint
    deepseek_key_path = local.p.deepseek_key_path
  })

  depends_on = [nomad_dynamic_host_volume.home]
}
