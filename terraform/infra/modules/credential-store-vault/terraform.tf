terraform {
  required_providers {
    # Creates the least-privilege Vault policy + periodic token that Boundary
    # authenticates the credential store with.
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
    # Registers the Vault credential store in the Boundary project scope.
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
  }
}
