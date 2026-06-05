# Downloadable connection artifact: a ready-to-paste ~/.ssh/config block for the
# transparent-session path (the live-verified primary). Written under the root's
# gitignored generated/ dir. The CLI fallback (no Client Agent) is shown inline.
resource "local_file" "ssh_config" {
  filename        = "${path.root}/generated/${local.slug}.ssh-config"
  file_permission = "0600"
  content         = <<-EOT
    # Boundary transparent session for ${var.developer_email}
    # Requires: the Boundary Client Agent running + an SSO login to Boundary.
    # Paste into ~/.ssh/config, then `ssh ${boundary_alias_target.ws.value}`
    # (or point VSCode Remote-SSH at this Host).
    Host ${boundary_alias_target.ws.value}
        User ${var.workspace_user}

    # CLI fallback (no Client Agent):
    #   boundary connect ssh -target-id ${boundary_target.ws.id}
  EOT
}
