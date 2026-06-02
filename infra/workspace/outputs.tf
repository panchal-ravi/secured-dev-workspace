output "workspace_target_ids" {
  description = "Map of <developer>/<workspace> => Boundary target id (use with `boundary connect -target-id`)"
  value       = { for k, t in boundary_target.ws : k => t.id }
}

output "workspace_ssh_ports" {
  description = "Map of <developer>/<workspace> => static SSH host port"
  value       = { for k, u in local.workspace_units : k => u.ssh_port }
}

output "namespaces" {
  description = "Nomad namespaces created (one per project)"
  value       = sort([for ns in nomad_namespace.project : ns.name])
}

output "managed_group_ids" {
  description = "Map of developer => Boundary OIDC managed-group id"
  value       = { for k, g in boundary_managed_group.dev : k => g.id }
}

output "workspace_aliases" {
  description = "Map of <developer>/<workspace> => Boundary target alias (the `ssh <alias>` / VSCode Remote-SSH Host)"
  value       = { for k, a in boundary_alias_target.ws : k => a.value }
}
