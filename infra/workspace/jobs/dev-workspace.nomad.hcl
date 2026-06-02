# Rendered per workspace by infra/workspace/main.tf (templatefile). Single Docker
# task: a persistent /home/dev (dynamic host volume), the Vault SSH CA public key
# installed as TrustedUserCAKeys (only Boundary-injected, CA-signed certs log in —
# no static authorized_keys), and a first-boot clone of the PUBLIC project repo.
# The SSH port binds on the host but is NOT exposed on the NLB — only the
# co-located Boundary worker (127.0.0.1) reaches it.
job "${job_name}" {
  namespace   = "${namespace}"
  datacenters = ["dc1"]
  type        = "service"

  group "workspace" {
    count = 1

    network {
      port "ssh" {
        static = ${ssh_port}
        to     = 22
      }
    }

    # Persistent home — the dynamic host volume created in main.tf.
    volume "home" {
      type            = "host"
      source          = "${volume_name}"
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
      }

      volume_mount {
        volume      = "home"
        destination = "/home/dev"
        read_only   = false
      }

      # Workload-identity federation: Nomad signs a per-task JWT (aud=vault.io from
      # the agent default_identity) and exchanges it at Vault's jwt-nomad auth
      # method for a short, read-only token used by the template below.
      vault {
        role = "dev-workspace"
      }

      # Vault SSH CA public key -> the file sshd trusts (TrustedUserCAKeys). Static
      # authorized_keys are gone; only Boundary-injected, CA-signed certs log in.
      template {
        destination = "local/trusted_ca.pub"
        perms       = "0644"
        change_mode = "noop"
        data        = <<EOH
{{ with secret "ssh-client-signer/config/ca" }}{{ .Data.public_key }}{{ end }}
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

# Migration cleanup: drop any pre-JIT static authorized_keys left on the
# persistent home volume. sshd is also set AuthorizedKeysFile=none so these are
# already inert — this just keeps the volume clean.
rm -f /home/dev/.ssh/authorized_keys /home/dev/.ssh/authorized_keys2

# The dynamic host volume mounts root-owned on first boot; hand it to the dev
# user BEFORE the clone (run as dev) so the clone can write into it.
chown dev:dev /home/dev

# Clone the project repo on first boot only (public repo over HTTPS, no creds).
if [ ! -d /home/dev/project/.git ]; then
  sudo -u dev git clone ${git_repo_url} /home/dev/project || true
fi
chown -R dev:dev /home/dev

exec /usr/sbin/sshd -D -e
EOH
      }

      resources {
        cpu    = 500
        memory = 1024
      }
    }
  }
}
