terraform {
  required_version = ">= 1.7"

  required_providers {
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
    nomad = {
      source  = "hashicorp/nomad"
      version = "~> 2.4"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "~> 4.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}
