variable "nomad_ca_pem" {
  description = <<-EOT
    Nomad's self-signed TLS cert (it is its own CA). Vault uses it to validate the
    HTTPS JWKS fetch when verifying Nomad workload-identity JWTs (WIF). Comes from
    the base module output `nomad_ca_pem`.
  EOT
  type        = string
}

variable "nomad_jwks_url" {
  description = <<-EOT
    Nomad's JWKS endpoint Vault calls to fetch the workload-identity signing keys.
    The Vault SERVER does this fetch and is co-located with Nomad, so it reaches it
    locally; the Nomad cert carries a 127.0.0.1 SAN, so jwks_ca_pem validation passes.
  EOT
  type        = string
  default     = "https://127.0.0.1:4646/.well-known/jwks.json"
}
