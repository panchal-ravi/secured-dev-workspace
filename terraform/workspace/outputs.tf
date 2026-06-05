output "target_id" {
  description = "Boundary ssh target id (use with `boundary connect ssh -target-id`)."
  value       = boundary_target.ws.id
}

output "alias" {
  description = "Boundary target alias (the `ssh <alias>` / VSCode Remote-SSH Host)."
  value       = boundary_alias_target.ws.value
}

output "managed_group_id" {
  description = "Boundary OIDC managed-group id for this developer/workspace."
  value       = boundary_managed_group.dev.id
}

output "namespace" {
  description = "Nomad namespace the workspace runs in."
  value       = local.p.namespace
}

output "ssh_port" {
  description = "Static SSH host port for the workspace."
  value       = var.ssh_port
}

output "connect_command" {
  description = "CLI fallback connect command (no Boundary Client Agent)."
  value       = "boundary connect ssh -target-id ${boundary_target.ws.id}"
}

output "ssh_config_path" {
  description = "Path to the generated ~/.ssh/config snippet."
  value       = local_file.ssh_config.filename
}
