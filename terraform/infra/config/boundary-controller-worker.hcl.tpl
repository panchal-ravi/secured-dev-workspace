# Boundary all-in-one (controller + worker) configuration.
# Rendered by Terraform templatefile(); placeholders are filled at apply time.
disable_mlock = true

controller {
  name        = "boundary-allinone-controller"
  description = "Boundary all-in-one controller"

  database {
    url = "${db_url}"
  }

  # Boundary Enterprise requires a license. The license file is written to this
  # path by the bootstrap script (modules/boundary-allinone/templates/bootstrap.sh.tftpl).
  license = "file:///etc/boundary.d/boundary_license.hclic"
}

worker {
  name              = "boundary-allinone-worker"
  description       = "Boundary all-in-one worker"
  initial_upstreams = ["127.0.0.1:9201"]
  public_addr       = "${public_addr}"
}

# API listener (TLS terminated by Boundary; the NLB is a TCP pass-through).
listener "tcp" {
  address       = "0.0.0.0:9200"
  purpose       = "api"
  tls_disable   = false
  tls_cert_file = "/etc/boundary.d/tls/boundary.crt"
  tls_key_file  = "/etc/boundary.d/tls/boundary.key"
}

# Controller cluster listener (worker -> controller, loopback only).
# Must bind the exact address the combined worker dials in initial_upstreams
# ("127.0.0.1:9201"); Boundary rejects a mismatch (e.g. 0.0.0.0) in combined mode.
listener "tcp" {
  address = "127.0.0.1:9201"
  purpose = "cluster"
}

# Worker proxy listener (clients -> worker, fronted by the NLB).
listener "tcp" {
  address     = "0.0.0.0:9202"
  purpose     = "proxy"
  tls_disable = false
}

kms "aead" {
  purpose   = "root"
  aead_type = "aes-gcm"
  key       = "${root_key}"
  key_id    = "global_root"
}

kms "aead" {
  purpose   = "worker-auth"
  aead_type = "aes-gcm"
  key       = "${worker_auth_key}"
  key_id    = "global_worker-auth"
}

kms "aead" {
  purpose   = "recovery"
  aead_type = "aes-gcm"
  key       = "${recovery_key}"
  key_id    = "global_recovery"
}
