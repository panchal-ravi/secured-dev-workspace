# Least-privilege policy granting only the capabilities Boundary needs to manage
# its own token lifecycle and the leases of credentials it brokers. This is
# HashiCorp's documented base policy for a Boundary Vault credential store;
# secret-engine paths for specific credential libraries get added in later phases.
resource "vault_policy" "boundary" {
  name = "boundary-credential-store"

  policy = <<-HCL
    path "auth/token/lookup-self" {
      capabilities = ["read"]
    }
    path "auth/token/renew-self" {
      capabilities = ["update"]
    }
    path "auth/token/revoke-self" {
      capabilities = ["update"]
    }
    path "sys/leases/renew" {
      capabilities = ["update"]
    }
    path "sys/leases/revoke" {
      capabilities = ["update"]
    }
    path "sys/capabilities-self" {
      capabilities = ["update"]
    }
  HCL
}

# Periodic, orphan, renewable token bound to that policy. Boundary requires a
# periodic token so it can self-renew it indefinitely; orphan so its lifecycle is
# independent of the root token that created it.
resource "vault_token" "boundary" {
  policies          = [vault_policy.boundary.name]
  period            = var.boundary_token_period
  no_parent         = true
  renewable         = true
  no_default_policy = true

  metadata = {
    purpose = "boundary-credential-store"
  }
}
