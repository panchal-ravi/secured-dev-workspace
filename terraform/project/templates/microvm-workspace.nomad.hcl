# microVM variant of the dev-workspace job template: identical to dev-workspace
# except it schedules on the "microvm" Nomad node pool and runs the task under the
# Kata Containers runtime (see the node_pool and config.runtime additions below).
# The SAME OCI image runs, but Kata wraps it in a lightweight microVM with its own
# guest kernel behind a hardware-virtualization (KVM) boundary — the isolation
# upgrade over shared-kernel container namespaces, for running untrusted AI-agent
# code. Full dev-workspace parity otherwise (cert-only sshd, Claude Code, dynamic
# Git PAT, read-only DB MCP, persistent /home/dev over virtio-fs).
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
#   ssh_ca_path       — per-project Vault SSH CA config path (ssh/config/ca)
#   github_token_path — per-project Vault GitHub permission-set token path
#   mcp_kv_path       — per-project Vault KV path for the virtual-MCP coordinates (secret/data/projects/mcp)
#   llm_kv_path       — per-project Vault KV path for the LiteLLM virtual key (secret/data/projects/llm)
#   llm_base_url      — node-private LiteLLM gateway base URL (Claude Code's ANTHROPIC_BASE_URL)
#
# Per-workspace placeholders (escaped "$$" here; filled by the portal at create):
#   job_name ssh_port volume_name developer_email git_user_name
# consul-template "{{ ... }}" and bash "$(...)" are left untouched by both passes.
job "$${job_name}" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  node_pool   = "microvm" # schedule on the bare-metal microVM node pool (Kata needs /dev/kvm)
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
    # Bind-mounted into the Kata guest over virtio-fs (why the AMI pins the qemu
    # hypervisor + shared_fs = "virtio-fs").
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
        runtime    = "kata" # Kata Containers runtime: runs this container inside a microVM (separate guest kernel, KVM boundary)
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

      # The project's REMOTE virtual MCP server on the central ContextForge gateway:
      # its URL + a per-project client bearer token, written to Vault KV by the
      # project tier's gateway orchestration (mcp-gateway.tf). Rendered to the
      # /secrets tmpfs over WIF; the entrypoint registers them with Claude.
      # change_mode=noop so a re-render NEVER restarts sshd; never lands on /home/dev.
      template {
        destination = "secrets/mcp-url"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${mcp_kv_path}" }}{{ .Data.data.url }}{{ end }}
EOH
      }

      template {
        destination = "secrets/mcp-token"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${mcp_kv_path}" }}{{ .Data.data.token }}{{ end }}
EOH
      }

      # Per-project LiteLLM virtual key (KV v2), minted into the /secrets tmpfs over
      # WIF. Claude Code is pointed at the shared LiteLLM gateway (managed-settings.json
      # below) and reads this via its apiKeyHelper (/usr/local/bin/llm-key), so the key
      # is read at call time and never lands on the persistent /home/dev. This is a
      # SCOPED virtual key (allowed models + budget + rpm), NOT the real provider key —
      # that lives only on the gateway. 0644 so the dev user (Claude runs as dev) can
      # read it; change_mode=noop so a re-render NEVER restarts sshd.
      template {
        destination = "secrets/llm-key"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "${llm_kv_path}" }}{{ .Data.data.virtual_key }}{{ end }}
EOH
      }

      # Claude Code's enterprise-managed settings, rendered with this project's
      # LiteLLM gateway URL (llm_base_url) and installed by the entrypoint to
      # /etc/claude-code/managed-settings.json — OUTSIDE /home/dev (which the
      # persistent volume shadows) so it is enforced and the developer can't re-point
      # the model. Non-secret (the key is served separately by apiKeyHelper), so it is
      # static data; the model names match the gateway model_list. change_mode=noop.
      template {
        destination = "local/managed-settings.json"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{
  "apiKeyHelper": "/usr/local/bin/llm-key",
  "env": {
    "DISABLE_AUTOUPDATER": "1",
    "ANTHROPIC_BASE_URL": "${llm_base_url}",
    "ANTHROPIC_MODEL": "deepseek-v4-pro",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek-v4-pro",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek-v4-pro",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek-v4-flash",
    "CLAUDE_CODE_SUBAGENT_MODEL": "deepseek-v4-flash",
    "CLAUDE_CODE_EFFORT_LEVEL": "max"
  }
}
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

# Install Claude Code's enterprise-managed settings (rendered with this project's
# LiteLLM gateway URL). /etc/claude-code is created in the image; this enforces the
# governed gateway + model mapping and is outside the persistent /home/dev volume.
install -o root -g root -m 0644 /local/managed-settings.json /etc/claude-code/managed-settings.json

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

# Register the project's REMOTE virtual MCP server (demo-db) on the central
# ContextForge gateway for the dev user, user-scoped so it is available from any
# directory. The URL + per-project bearer token are rendered to /secrets over WIF
# (templates above). Idempotent — only add when not already present — and a
# registration failure WARNs without aborting the workspace (a missing MCP tool
# must never cost the developer their SSH session).
if ! sudo -u dev claude mcp list 2>/dev/null | grep -q 'demo-db'; then
  sudo -u dev claude mcp add --scope user --transport sse demo-db "$(cat /secrets/mcp-url)" \
    --header "Authorization: Bearer $(cat /secrets/mcp-token)" \
    || echo "WARN: failed to register remote demo-db MCP server" >&2
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
