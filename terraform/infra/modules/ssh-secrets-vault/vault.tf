# SSH secrets engine in signing (CA) mode. Vault holds the CA private key and
# signs short-lived SSH USER certificates; the workspace sshd trusts only this CA
# via TrustedUserCAKeys, so static authorized_keys are gone entirely.
resource "vault_mount" "ssh" {
  path        = "ssh-client-signer"
  type        = "ssh"
  description = "SSH client CA — signs short-lived dev-workspace user certs"
}

# Generate + hold the CA signing key inside Vault. ed25519 so OpenSSH accepts the
# signature without the deprecated ssh-rsa (SHA-1) algorithm.
resource "vault_ssh_secret_backend_ca" "ssh" {
  backend              = vault_mount.ssh.path
  generate_signing_key = true
  key_type             = "ed25519"
}

# Signing role: locks the cert to the "dev" principal, permits a pty + TCP port
# forwarding, short TTLs. Boundary supplies key_id = the authenticated developer's
# email for audit.
resource "vault_ssh_secret_backend_role" "dev_workspace" {
  name                    = "dev-workspace"
  backend                 = vault_mount.ssh.path
  key_type                = "ca"
  allow_user_certificates = true
  allow_user_key_ids      = true # Boundary stamps key_id = developer email (audit attribution)
  allowed_users           = "dev"
  default_user            = "dev"

  # permit-pty enables an interactive shell; permit-port-forwarding is REQUIRED for
  # VSCode Remote-SSH, which reaches its remote server over a direct-tcpip channel.
  # sshd enforces a certificate's extensions on top of the global AllowTcpForwarding,
  # so without this the forward is refused as "administratively prohibited" even
  # though a plain shell logs in fine.
  default_extensions = {
    permit-pty             = ""
    permit-port-forwarding = ""
  }

  ttl     = var.cert_ttl
  max_ttl = var.cert_max_ttl
}
