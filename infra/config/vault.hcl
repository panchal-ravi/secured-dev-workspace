# Vault single-node server. Written to /etc/vault.d/vault.hcl by the bootstrap
# script. File storage (single demo node, no HA). The NLB is a TCP pass-through,
# so Vault serves its self-signed cert end-to-end; clients skip-verify it
# (VAULT_SKIP_VERIFY=true).
ui            = true
disable_mlock = false

storage "file" {
  path = "/opt/vault/data"
}

listener "tcp" {
  address       = "0.0.0.0:8200"
  tls_cert_file = "/etc/vault.d/tls/vault.crt"
  tls_key_file  = "/etc/vault.d/tls/vault.key"
}

api_addr     = "https://127.0.0.1:8200"
license_path = "/etc/vault.d/vault_license.hclic"
