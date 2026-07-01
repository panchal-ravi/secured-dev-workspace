# ---------------------------------------------------------------------------
# Per-project Vault SSH certificate authority, mounted at `ssh` inside the
# project's Vault namespace. Each project gets its OWN CA, signing role and read
# scope — isolation is by Vault namespace, so the mount path needs no project
# prefix.
# ---------------------------------------------------------------------------

# SSH secrets engine in signing (CA) mode for this project. Vault holds the CA
# private key and signs short-lived SSH USER certs; the project's workspace sshd
# trusts only this CA via TrustedUserCAKeys.
resource "vault_mount" "ssh" {
  namespace   = vault_namespace.project.path
  path        = "ssh"
  type        = "ssh"
  description = "SSH client CA for project ${var.project_name} — signs short-lived workspace user certs"
}

# Generate + hold the CA signing key inside Vault. ed25519 so OpenSSH accepts the
# signature without the deprecated ssh-rsa (SHA-1) algorithm.
resource "vault_ssh_secret_backend_ca" "ssh" {
  namespace            = vault_namespace.project.path
  backend              = vault_mount.ssh.path
  generate_signing_key = true
  key_type             = "ed25519"
}

# Signing role: locks the cert to the workspace user, permits a pty + TCP port
# forwarding (permit-port-forwarding is REQUIRED for VSCode Remote-SSH's
# direct-tcpip channel), short TTLs. Boundary supplies key_id = the authenticated
# developer's email for audit.
resource "vault_ssh_secret_backend_role" "dev_workspace" {
  namespace               = vault_namespace.project.path
  name                    = "dev-workspace"
  backend                 = vault_mount.ssh.path
  key_type                = "ca"
  allow_user_certificates = true
  allow_user_key_ids      = true # Boundary stamps key_id = developer email (audit attribution)
  allowed_users           = var.workspace_user
  default_user            = var.workspace_user

  default_extensions = {
    permit-pty             = ""
    permit-port-forwarding = ""
  }

  ttl     = var.cert_ttl
  max_ttl = var.cert_max_ttl
}

# ---------------------------------------------------------------------------
# Boundary credential-store token: a dedicated least-privilege periodic token
# (NOT root) Boundary authenticates to Vault with. Grants only the token
# self-lifecycle + lease paths Boundary needs, plus create/update on THIS
# project's signing endpoint.
# ---------------------------------------------------------------------------
resource "vault_policy" "boundary" {
  namespace = vault_namespace.project.path
  name      = "boundary-${var.project_name}-cred-store"

  policy = <<-HCL
    path "auth/token/lookup-self" {
      capabilities = ["read"]
    }
    path "auth/token/renew-self" {
      capabilities = ["update"]
    }
    path "auth/token/revoke-self" {
      capabilities = ["update"]
    }
    path "sys/leases/renew" {
      capabilities = ["update"]
    }
    path "sys/leases/revoke" {
      capabilities = ["update"]
    }
    path "sys/capabilities-self" {
      capabilities = ["update"]
    }
    path "${vault_mount.ssh.path}/sign/${vault_ssh_secret_backend_role.dev_workspace.name}" {
      capabilities = ["create", "update"]
    }
  HCL
}

# Periodic, orphan, renewable token bound to that policy. Boundary requires a
# periodic token so it can self-renew indefinitely; orphan so its lifecycle is
# independent of the root token that created it.
resource "vault_token" "boundary" {
  namespace         = vault_namespace.project.path
  policies          = [vault_policy.boundary.name]
  period            = var.boundary_token_period
  no_parent         = true
  renewable         = true
  no_default_policy = true

  metadata = {
    purpose = "boundary-credential-store"
    project = var.project_name
  }
}

# ---------------------------------------------------------------------------
# Per-project Nomad↔Vault workload-identity federation (WIF). A role on the
# SHARED `jwt-nomad` backend (foundation tier), scoped to a read-only policy that
# exposes ONLY this project's SSH CA public key. The workspace job names this
# role in its `vault { role = ... }` stanza.
# ---------------------------------------------------------------------------
resource "vault_policy" "nomad_ca_read" {
  namespace = vault_namespace.project.path
  name      = "nomad-${var.project_name}-ca-read"

  policy = <<-HCL
    path "${vault_mount.ssh.path}/config/ca" {
      capabilities = ["read"]
    }
  HCL
}

# WIF grant: the read-only DB role path (use case B1). Now read by the per-project
# demo-db-mcp service (demo-db-mcp.tf) — the centralized MCP server that holds the
# shared connection — using this same project WIF role.
resource "vault_policy" "nomad_db_creds" {
  namespace = vault_namespace.project.path
  name      = "nomad-${var.project_name}-db-creds"

  policy = <<-HCL
    path "${vault_mount.database.path}/creds/${vault_database_secret_backend_role.dev_workspace_ro.name}" {
      capabilities = ["read"]
    }
  HCL
}

# WIF grant: the workspace task may READ the per-project MCP coordinates (the
# virtual-server URL + client bearer token) written by the gateway orchestration
# (mcp-gateway.tf) to secret/projects/mcp. KV v2 ⇒ /data/ prefix. The Vault
# namespace isolates the project, so the KV path carries no project segment.
resource "vault_policy" "nomad_mcp_read" {
  namespace = vault_namespace.project.path
  name      = "nomad-${var.project_name}-mcp-read"

  policy = <<-HCL
    path "${local.f.kv_mount_path}/data/projects/mcp" {
      capabilities = ["read"]
    }
  HCL
}

# Role name = project name. bound_audiences MUST match the Nomad agent
# default_identity.aud (config/nomad.hcl) and the job identity.aud — all three are
# the fixed literal "vault.io"; a mismatch fails JWT verification with a silent 403.
resource "vault_jwt_auth_backend_role" "project" {
  namespace               = vault_namespace.project.path
  backend                 = vault_jwt_auth_backend.nomad.path
  role_name               = var.project_name
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  token_policies          = [vault_policy.nomad_ca_read.name, vault_policy.nomad_github_token.name, vault_policy.nomad_db_creds.name, vault_policy.nomad_llm_read.name, vault_policy.nomad_mcp_read.name]
  token_ttl               = 1800
  token_max_ttl           = 3600
  token_type              = "service"
}
