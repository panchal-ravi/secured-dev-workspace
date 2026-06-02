# ---------------------------------------------------------------------------
# Flatten developers x workspaces into the unit that drives every per-workspace
# resource (dynamic host volume, Nomad job, Boundary target+role). This is the
# shape the future portal will generate one entry at a time.
# ---------------------------------------------------------------------------
locals {
  workspace_units = merge([
    for dev_name, dev in var.developers : {
      for ws_name, ws in dev.workspaces :
      "${dev_name}/${ws_name}" => {
        dev_name     = dev_name
        ws_name      = ws_name
        email        = dev.email
        project      = ws.project
        ssh_port     = ws.ssh_port
        git_repo_url = var.projects[ws.project].git_repo_url
        image        = var.projects[ws.project].image
      }
    }
  ]...)

  active_projects = toset([for u in local.workspace_units : u.project])

  # alice/main -> alice-main (DNS/Nomad-safe id reused for every resource name)
  unit_slug = { for k, _ in local.workspace_units : k => replace(k, "/", "-") }

  ssh_ports = [for u in local.workspace_units : u.ssh_port]
}

# Hard guard: workspaces share one node, so SSH host ports must be unique.
resource "terraform_data" "validations" {
  lifecycle {
    precondition {
      condition     = length(local.ssh_ports) == length(distinct(local.ssh_ports))
      error_message = "Each workspace must use a DISTINCT ssh_port (they bind on the same shared node)."
    }
  }
}

# Project = Nomad namespace.
resource "nomad_namespace" "project" {
  for_each    = local.active_projects
  name        = each.value
  description = "Project namespace (dev-workspace PoC)"
}

# Persistent /home/dev per workspace. Dynamic host volumes (Nomad 1.10+) survive
# stop/start + reboot WITHOUT a static client-config host_volume (which would
# need a nomad.hcl change + instance replace per volume). The built-in `mkdir`
# plugin creates the backing dir; the job's entrypoint chowns it to the dev user.
# NOTE: lives on the node root volume — survives stop/start + reboot, NOT an
# instance replacement (CSI/EBS durable storage is the roadmap fix).
resource "nomad_dynamic_host_volume" "home" {
  for_each  = local.workspace_units
  name      = "home-${local.unit_slug[each.key]}"
  namespace = each.value.project
  plugin_id = "mkdir"

  capability {
    access_mode     = "single-node-writer"
    attachment_mode = "file-system"
  }

  depends_on = [nomad_namespace.project]
}

# One workspace job per unit.
resource "nomad_job" "workspace" {
  for_each = local.workspace_units

  detach           = false # wait for the deployment to become healthy on apply
  purge_on_destroy = true

  jobspec = templatefile("${path.module}/jobs/dev-workspace.nomad.hcl", {
    job_name     = "ws-${local.unit_slug[each.key]}"
    namespace    = each.value.project
    image        = each.value.image
    ssh_port     = each.value.ssh_port
    volume_name  = nomad_dynamic_host_volume.home[each.key].name
    git_repo_url = each.value.git_repo_url
  })

  depends_on = [nomad_dynamic_host_volume.home]
}
