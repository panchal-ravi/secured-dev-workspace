# Nomad all-in-one: a single combined server + client agent.
# Written to /etc/nomad.d/nomad.hcl by the bootstrap script. The agent runs as
# root (see config/nomad.service) so the client's docker/exec drivers work.
region     = "global"
datacenter = "dc1"
name       = "nomad-allinone"
data_dir   = "/opt/nomad/data"
bind_addr  = "0.0.0.0"
log_level  = "INFO"
log_file   = "/var/log/nomad/nomad.log"

server {
  enabled          = true
  bootstrap_expect = 1
  license_path     = "/etc/nomad.d/license.hclic"
}

client {
  enabled = true
}

# The EBS CSI node plugin runs privileged (it stages/mounts the block device
# backing a workspace's /home/dev CSI volume), so the docker driver must allow it.
# NOTE: like the vault{} block below, this file is the cloud-init SOURCE only —
# aws_instance.this has lifecycle ignore_changes=all, so a clean destroy+recreate
# (not terraform apply on a running node) is what puts this into effect.
plugin "docker" {
  config {
    allow_privileged = true
  }
}

acl {
  enabled = true
}

ui {
  enabled = true
}

ports {
  http = 4646
  rpc  = 4647
  serf = 4648
}

# Self-signed cert that is its own CA. The NLB is a TCP pass-through, so Nomad
# serves this cert end-to-end; clients skip-verify it (NOMAD_SKIP_VERIFY=true).
# The cert carries the server.global.nomad / client.global.nomad SANs that
# verify_server_hostname requires for RPC between the combined server and client.
tls {
  http = true
  rpc  = true

  ca_file   = "/etc/nomad.d/tls/nomad.crt"
  cert_file = "/etc/nomad.d/tls/nomad.crt"
  key_file  = "/etc/nomad.d/tls/nomad.key"

  verify_server_hostname = true
  verify_https_client    = false
}

# Nomad↔Vault workload-identity federation (WIF). Nomad signs a per-task identity
# JWT (aud=vault.io); the task's vault{} block exchanges it at Vault's jwt-nomad
# auth method for a short, read-only token. Vault is co-located, so reach it
# locally (its cert carries a 127.0.0.1 SAN). NOTE: this file is the cloud-init
# SOURCE only — aws_instance.this has lifecycle ignore_changes=all, so editing it
# does NOT replace the instance and does NOT push to the running node. Apply the
# same block in-place on the node + restart Nomad separately.
vault {
  enabled               = true
  address               = "https://127.0.0.1:8200"
  jwt_auth_backend_path = "jwt-nomad"
  tls_skip_verify       = true

  default_identity {
    aud = ["vault.io"]
    ttl = "1h"
  }
}
