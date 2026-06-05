# GPU variant of the dev-workspace job template: identical to dev-workspace except
# it schedules on the "gpu" Nomad node pool and requests an NVIDIA T4 via the
# nomad-device-nvidia plugin (see the node_pool, resources.device and config.runtime
# additions below). Full dev-workspace parity otherwise (cert-only sshd, Claude Code,
# dynamic Git PAT, read-only DB MCP).
#
# Project-owned Nomad job template, published to Vault KV at project onboarding
# (terraform/project/kv.tf). At publish time templatestring() fills the
# PROJECT-STATIC values listed below in place; the PER-WORKSPACE placeholders are
# escaped in the source (doubled "$$") so they survive that pass untouched,
# leaving single-"$" placeholders the Developer Portal fills at create time.
# Single Docker task: a persistent /home/dev (dynamic host volume), the project's
# Vault SSH CA public key installed as TrustedUserCAKeys (only Boundary-injected,
# CA-signed certs log in — no static authorized_keys), git pre-configured for the
# logged-in developer with a short-lived Vault-minted GitHub App token as the push
# credential, and a first-boot clone of the project repo (private-capable). The
# SSH port binds on the host but is NOT exposed on the NLB — only the co-located
# Boundary worker (127.0.0.1) reaches it.
#
# Project-static placeholders (filled at onboarding by kv.tf):
#   namespace image git_repo_url
#   wif_role          — per-project Nomad↔Vault WIF role (= project name)
#   ssh_ca_path       — per-project Vault SSH CA config path (ssh/<project>/config/ca)
#   github_token_path — per-project Vault GitHub permission-set token path
#   db_creds_path     — per-project Vault read-only DB creds path (database/<project>/creds/dev-workspace-ro)
#   db_endpoint       — host:port the container uses to reach the demo-db (node IP + demo-db port)
#   deepseek_key_path — per-project Vault KV path for the DeepSeek API key (secret/data/projects/<project>/deepseek)
#
# Per-workspace placeholders (escaped "$$" here; filled by the portal at create):
#   job_name ssh_port volume_name developer_email git_user_name
# consul-template "{{ ... }}" and bash "$(...)" are left untouched by both passes.
job "$${job_name}" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  node_pool   = "gpu" # schedule on the GPU node pool; the device request below pins it to the T4 node
  type        = "service"

  group "workspace" {
    count = 1

    network {
      port "ssh" {
        static = $${ssh_port}
        to     = 22
      }
    }

    # Persistent home — the dynamic host volume created by the dev-workspace tier.
    volume "home" {
      type            = "host"
      source          = "$${volume_name}"
      read_only       = false
      access_mode     = "single-node-writer"
      attachment_mode = "file-system"
    }

    task "workspace" {
      driver = "docker"

      config {
        image      = "${image}"
        force_pull = true # always pull from the (public) Docker Hub registry
        ports      = ["ssh"]
        command    = "/local/entrypoint.sh"
        runtime    = "nvidia" # nvidia-container-toolkit runtime: mounts the driver + GPU devices into the container
      }

      volume_mount {
        volume      = "home"
        destination = "/home/dev"
        read_only   = false
      }

      # Workload-identity federation: Nomad signs a per-task JWT (aud=vault.io from
      # the agent default_identity) and exchanges it at Vault's jwt-nomad auth
      # method for a short, read-only token used by the template below. The role is
      # this project's WIF role.
      vault {
        role = "${wif_role}"
      }

      # Vault SSH CA public key -> the file sshd trusts (TrustedUserCAKeys). Static
      # authorized_keys are gone; only Boundary-injected, CA-signed certs log in.
      template {
        destination = "local/trusted_ca.pub"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${ssh_ca_path}" }}{{ .Data.public_key }}{{ end }}
EOH
      }

      # Short-lived (1h, non-renewable) GitHub App installation token, minted per
      # render from the project's PRE-SCOPED Vault permission set over WIF (no
      # params needed — installation id + contents:write are baked into the set).
      # consul-template re-renders before expiry to keep it fresh across a long
      # session; change_mode=noop so re-render NEVER restarts sshd. Rendered to the
      # /secrets tmpfs only — the token never lands on the persistent /home/dev.
      template {
        destination = "secrets/git-token"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${github_token_path}" }}{{ .Data.token }}{{ end }}
EOH
      }

      # Per-session, read-only Postgres credential (use case B1), minted over WIF
      # from the project's database engine. consul-template renders a full
      # connection URI to the /secrets tmpfs; the Postgres MCP server reads it at
      # spawn. change_mode=noop so a re-render NEVER restarts sshd. The credential
      # never lands on the persistent /home/dev.
      template {
        destination = "secrets/db-uri"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${db_creds_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_endpoint}/appdb?sslmode=disable{{ end }}
EOH
      }

      # DeepSeek API key (KV v2), minted into the /secrets tmpfs over WIF. The
      # baked-in Claude Code CLI is pointed at DeepSeek (image managed-settings.json)
      # and reads this via its apiKeyHelper (/usr/local/bin/deepseek-key), so the key
      # is read at call time and never lands on the persistent /home/dev. 0644 so the
      # dev user (Claude runs as dev) can read it; change_mode=noop so a re-render
      # NEVER restarts sshd.
      template {
        destination = "secrets/deepseek-key"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${deepseek_key_path}" }}{{ .Data.data.api_key }}{{ end }}
EOH
      }

      template {
        destination = "local/entrypoint.sh"
        perms       = "0755"
        data        = <<EOH
#!/bin/bash
set -euo pipefail

# Trust the Vault SSH CA (re-rendered every launch from the template above).
install -o root -g root -m 0644 /local/trusted_ca.pub /etc/ssh/trusted_ca.pub

# The dynamic host volume mounts root-owned on first boot; hand it to the dev
# user BEFORE the clone (run as dev) so the clone can write into it.
chown dev:dev /home/dev

# Pre-configure git for the logged-in developer (global config, set as dev), so
# commits are authored by them with no manual `git config` step. Idempotent.
sudo -u dev git config --global user.email "$${developer_email}"
sudo -u dev git config --global user.name  "$${git_user_name}"

# Git push credential: a read-only helper that serves the Vault-minted GitHub App
# token (refreshed in /secrets/git-token by the template above). Scoped to
# github.com only. The token is read at call time, so it always reflects the
# latest re-render. $(...) strips the trailing newline, so the password is clean.
cat > /local/git-credential-helper <<'HELPER'
#!/bin/bash
echo username=x-access-token
echo "password=$(cat /secrets/git-token)"
HELPER
chmod 0755 /local/git-credential-helper
sudo -u dev git config --global 'credential.https://github.com.helper' /local/git-credential-helper

# Register the Postgres MCP server (use case B1) for the dev user, user-scoped so
# it is available from any directory. The wrapper /usr/local/bin/pg-mcp (baked
# into the workspace image) feeds DATABASE_URI from /secrets/db-uri (the
# Vault-minted, per-session credential) at spawn. Idempotent — only add when not
# already present — and a registration failure WARNs without aborting the
# workspace (a missing MCP tool must never cost the developer their SSH session).
if ! sudo -u dev claude mcp list 2>/dev/null | grep -q 'demo-db'; then
  sudo -u dev claude mcp add --scope user demo-db -- /usr/local/bin/pg-mcp \
    || echo "WARN: failed to register demo-db MCP server" >&2
fi

# Clone the project repo on first boot only. Private repos work: the credential
# helper supplies the GitHub App token for the HTTPS github.com clone.
if [ ! -d /home/dev/project/.git ]; then
  sudo -u dev git clone ${git_repo_url} /home/dev/project || true
fi
chown -R dev:dev /home/dev

exec /usr/sbin/sshd -D -e
EOH
      }

      resources {
        cpu    = 2000
        memory = 4096

        # The nomad-device-nvidia plugin fingerprints the T4 and injects
        # NVIDIA_VISIBLE_DEVICES into the task; with config.runtime="nvidia" above,
        # the GPU is exposed inside the container (nvidia-smi / CUDA work).
        device "nvidia/gpu" {
          count = 1
        }
      }
    }
  }
}
