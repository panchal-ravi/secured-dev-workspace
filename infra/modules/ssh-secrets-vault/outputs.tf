output "ca_public_key" {
  description = "SSH CA public key — goes into the workspace sshd TrustedUserCAKeys file"
  value       = vault_ssh_secret_backend_ca.ssh.public_key
}

output "sign_path" {
  description = "Vault path Boundary signs SSH certs against"
  value       = "${vault_mount.ssh.path}/sign/${vault_ssh_secret_backend_role.dev_workspace.name}"
}

output "ssh_backend_path" {
  description = "SSH secrets engine mount path"
  value       = vault_mount.ssh.path
}
