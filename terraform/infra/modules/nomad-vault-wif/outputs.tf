output "backend_path" {
  description = "Path of the jwt-nomad auth method (project WIF roles attach to this backend)"
  value       = vault_jwt_auth_backend.nomad.path
}
