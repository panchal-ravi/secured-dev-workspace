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

  validity_period_hours = 43800

  allowed_uses = [
    "digital_signature",
    "key_encipherment",
    "server_auth",
    "client_auth",
  ]
}
