# Self-signed server cert for the Boundary API listener. The NLB is a TCP
# pass-through, so Boundary serves this cert end-to-end; clients skip-verify it
# (boundary CLI -tls-skip-verify). The cert's SAN is the NLB DNS name, which is
# instance-independent — so there is no cert <-> instance dependency cycle.
resource "tls_private_key" "boundary" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "boundary" {
  private_key_pem = tls_private_key.boundary.private_key_pem

  dns_names    = ["localhost", aws_lb.this.dns_name]
  ip_addresses = ["127.0.0.1"]

  subject {
    common_name         = aws_lb.this.dns_name
    organization        = "Demo Organization"
    organizational_unit = "Boundary"
  }

  validity_period_hours = 8760 # 365 days — under Apple/macOS's 398-day TLS cap so
  # Go's platform verifier accepts it (the Boundary Client Agent has no -tls-insecure
  # escape; a 1825-day cert is rejected as "not standards compliant"). See go#51991.

  allowed_uses = [
    "digital_signature",
    "key_encipherment",
    "server_auth",
    "client_auth",
  ]
}

# Self-signed cert for the Vault API listener. Same TCP-passthrough NLB story as
# the Boundary cert: Vault serves this end-to-end and clients skip-verify it.
resource "tls_private_key" "vault" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "vault" {
  private_key_pem = tls_private_key.vault.private_key_pem

  dns_names    = ["localhost", aws_lb.this.dns_name]
  ip_addresses = ["127.0.0.1"]

  subject {
    common_name         = aws_lb.this.dns_name
    organization        = "Demo Organization"
    organizational_unit = "Vault"
  }

  validity_period_hours = 8760 # 365 days — under macOS's 398-day TLS cap (see the
  # Boundary cert above): a Go platform verifier rejects longer-lived self-signed certs.

  allowed_uses = [
    "digital_signature",
    "key_encipherment",
    "server_auth",
    "client_auth",
  ]
}

# Self-signed cert for the Nomad HTTP+RPC API. Same TCP-passthrough NLB story as
# the Boundary cert. The server.global.nomad / client.global.nomad SANs are
# required by Nomad's RPC verify_server_hostname for the combined server<->client.
resource "tls_private_key" "nomad" {
  algorithm = "RSA"
  rsa_bits  = 2048
}

resource "tls_self_signed_cert" "nomad" {
  private_key_pem = tls_private_key.nomad.private_key_pem

  dns_names    = ["localhost", "server.global.nomad", "client.global.nomad", aws_lb.this.dns_name]
  ip_addresses = ["127.0.0.1"]

  subject {
    common_name         = aws_lb.this.dns_name
    organization        = "Demo Organization"
    organizational_unit = "Nomad"
  }

  validity_period_hours = 8760 # 365 days — under macOS's 398-day TLS cap (see the
  # Boundary cert above): a Go platform verifier rejects longer-lived self-signed certs.

  allowed_uses = [
    "digital_signature",
    "key_encipherment",
    "server_auth",
    "client_auth",
  ]
}
