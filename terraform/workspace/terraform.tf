terraform {
  # templatestring() (used to render the KV-stored jobspec) requires >= 1.9.
  required_version = ">= 1.9"

  required_providers {
    nomad = {
      source  = "hashicorp/nomad"
      version = "~> 2.6" # 2.5+ for the dynamic-host-volume resource
    }
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
  }
}
