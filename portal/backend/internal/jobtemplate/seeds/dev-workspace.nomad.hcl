# Project-owned Nomad job template, published to Vault KV at project onboarding
# (terraform/project/kv.tf). At publish time templatestring() fills the
# PROJECT-STATIC values listed below in place; the PER-WORKSPACE placeholders are
# escaped in the source (doubled "$$") so they survive that pass untouched,
# leaving single-"$" placeholders the Developer Portal fills at create time.
# Single Docker task: a persistent /home/dev (durable EBS CSI volume), the project's
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
#   ssh_ca_path       — per-project Vault SSH CA config path (ssh/config/ca)
#   github_token_path — per-project Vault GitHub permission-set token path
# This base template is AGENT-AGNOSTIC: the coding agent is chosen at flavor create,
# and its wiring (e.g. Claude Code's LiteLLM virtual key + managed-settings.json, or
# Bob's ~/.bob config) plus any MCP servers are injected at the @project-addons
# markers below — none of it is baked into the base.
#
# Per-workspace placeholders (escaped "$$" here; filled by the portal at create):
#   job_name ssh_port volume_name developer_email git_user_name
# consul-template "{{ ... }}" and bash "$(...)" are left untouched by both passes.
job "${job_name}" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  type        = "service"

  group "workspace" {
    count = 1

    # Recover onto a healthy node after node failure; the durable EBS home volume
    # reattaches in-AZ. The external Nomad→Boundary host-sync then re-points this
    # workspace's Boundary host at the replacement node.
    reschedule {
      unlimited      = true
      delay          = "15s"
      delay_function = "constant"
    }
    migrate {
      max_parallel = 1
      health_check = "task_states"
    }

    network {
      port "ssh" {
        static = ${ssh_port}
        to     = 22
      }
    }

    # Nomad-native service registration. address_mode=host advertises the node IP +
    # the static host SSH port (not the bridge alloc IP). Tagged so the external
    # Nomad→Boundary host-sync keeps this workspace's Boundary host address pointed
    # at whatever node the alloc currently runs on across reschedules.
    service {
      name         = "${job_name}"
      provider     = "nomad"
      port         = "ssh"
      address_mode = "host"
      tags         = ["service-type=workspace", "project=${namespace}"]
      check {
        type     = "tcp"
        port     = "ssh"
        interval = "10s"
        timeout  = "2s"
      }
    }

    # Persistent home — a durable per-workspace EBS volume (AWS EBS CSI driver),
    # provisioned by the portal at create. Survives node crash / instance
    # replacement; reattaches to a replacement node in the same AZ.
    volume "home" {
      type            = "csi"
      source          = "${volume_name}"
      read_only       = false
      access_mode     = "single-node-writer"
      attachment_mode = "file-system"
    }

    # Per-project shared volumes (EFS) the developer selected at launch, each a
    # multi-node-multi-writer CSI volume. Empty when none selected. Rendered
    # per-workspace by the portal (workspace/service.go).
    ${shared_volume_defs}

    task "workspace" {
      driver = "docker"

      config {
        image      = "${image}"
        force_pull = true # always pull from the (public) Docker Hub registry
        ports      = ["ssh"]
        command    = "/local/entrypoint.sh"
      }

      volume_mount {
        volume      = "home"
        destination = "/home/dev"
        read_only   = false
      }

      # Mounts for the developer's selected shared volumes (empty when none).
      ${shared_volume_mounts}

      # Workload-identity federation: Nomad signs a per-task JWT (aud=vault.io from
      # the agent default_identity) and exchanges it at Vault's jwt-nomad auth
      # method for a short, read-only token used by the template below. The role is
      # this project's WIF role.
      vault {
        namespace = "${vault_namespace}"
        role      = "${wif_role}"
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

      # Project add-on secret templates are injected here by the portal when a
      # project-admin attaches MCP servers (from the catalog) or extra secret engines
      # to this flavor (structured template extension). The base template ships with
      # NO MCP wiring — MCP is a per-project choice.
      # Per-project coding-agent secret templates (e.g. Claude Code's LiteLLM virtual
      # key + managed-settings.json) are injected here by the portal based on the agent
      # chosen at flavor create — the base template is agent-agnostic.
      # @project-addons:secrets

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
sudo -u dev git config --global user.email "${developer_email}"
sudo -u dev git config --global user.name  "${git_user_name}"

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

# Coding-agent setup + MCP registrations are injected here by the portal based on the
# agent chosen at flavor create (e.g. Claude Code installs its managed-settings and runs
# `claude mcp add`; Bob writes ~/.bob/mcp_settings.json). Each MCP add is user-scoped and
# idempotent; a failure WARNs without aborting the workspace (a missing MCP tool must
# never cost the developer their SSH session).
# @project-addons:entrypoint

# Point package/build caches at a shared volume when a project-admin has created
# one mounted at /shared/cache and the developer kept it selected. Guarded on the
# directory existing, so a workspace with no shared cache volume is unaffected. The
# EFS access point overrides POSIX ids, so every workspace sharing the volume reads
# and writes the same warm cache.
if [ -d /shared/cache ]; then
  sudo -u dev mkdir -p /shared/cache/go /shared/cache/go-build /shared/cache/npm /shared/cache/pip
  cat > /etc/profile.d/shared-cache.sh <<'CACHEENV'
export GOMODCACHE=/shared/cache/go
export GOCACHE=/shared/cache/go-build
export npm_config_cache=/shared/cache/npm
export PIP_CACHE_DIR=/shared/cache/pip
CACHEENV
  chmod 0644 /etc/profile.d/shared-cache.sh
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
      }
    }
  }
}
