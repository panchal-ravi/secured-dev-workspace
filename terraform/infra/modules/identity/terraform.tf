terraform {
  required_providers {
    # No first-class provider exists for IBM Verify application management, so we
    # drive its REST API declaratively with the generic REST provider.
    restapi = {
      source  = "Mastercard/restapi"
      version = "~> 1.20"
    }
    boundary = {
      source  = "hashicorp/boundary"
      version = "~> 1.2"
    }
    nomad = {
      source  = "hashicorp/nomad"
      version = "~> 2.4"
    }
    # Used to GET each created app back so its generated client credentials are
    # available in the same apply (Verify's POST returns only an `_links` stub).
    http = {
      source  = "hashicorp/http"
      version = "~> 3.4"
    }
  }
}
