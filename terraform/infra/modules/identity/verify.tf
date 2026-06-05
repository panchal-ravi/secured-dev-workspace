# ---------------------------------------------------------------------------
# IBM Verify OIDC applications (one per relying party).
#
# Body modeled on a real Verify OIDC ("Connect", templateId 998) application.
# Key choices:
#   - grantTypes.authorizationCode = "true" (standard web/SSO login flow)
#   - doNotGenerateClientSecret = "false" -> Verify mints a client secret that
#     Boundary/Nomad use as confidential clients
#   - token.attributeMappings emits the `groups` claim that drives
#     readonly/admin authorization downstream
#
# The `groups` mapping (sourceId "4" + lowercase) matches a confirmed Verify
# OIDC app in this tenant. `email` is delivered via the standard `email` scope
# (requested by Boundary/Nomad), so it needs no explicit attribute mapping. If
# your tenant exposes groups under a different sourceId, adjust
# local.group_attribute_mappings. See docs/specs/phase-6-ibm-verify-oidc.md.
# ---------------------------------------------------------------------------

locals {
  oidc_grant_types = {
    authorizationCode = "true"
    implicit          = "false"
    clientCredentials = "false"
    ropc              = "false"
    jwtBearer         = "false"
    deviceFlow        = "false"
    tokenExchange     = "false"
    policyAuth        = "false"
  }

  # `groups` is sourced from the tenant's group-membership attribute (sourceId
  # "4") and lowercased, mirroring a confirmed Verify OIDC app. Our Verify group
  # names are already lowercase, so `lowercase` is a no-op for the match.
  #
  # This same mapping is applied in TWO places (Verify keeps them separate):
  #   - providers.oidc.token.attributeMappings (below) -> the JWT *access token*
  #   - the top-level `attributeMappings` app field -> the *ID token + userinfo*
  # Boundary/Nomad only read claims from the ID token / userinfo, so the
  # top-level mapping is the one that actually drives group->role authz; the
  # access-token copy is harmless. The top-level field can't be set via the
  # create body's restricted shape or PATCH/POST (405), so it's applied by the
  # post-create full-object PUT — see scripts/set_app_fields.py below.
  group_attribute_mappings = [
    { targetName = "groups", sourceId = "4", function = { name = "lowercase" } },
  ]

  # Displayed "Company Name" on each app (matches the other apps in this tenant).
  # Set via a post-create full-object PUT, not the create body — see the
  # terraform_data.*_app_fields resources below.
  company_name = "HashiCorp"

  # Helper to assemble an OIDC application body. Audience == app name keeps the
  # access-token audience stable and predictable for Nomad's bound_audiences.
  boundary_app_body = {
    name       = "secured-codespace-boundary"
    templateId = "998"
    # Verify creates apps DISABLED unless told otherwise; this top-level boolean
    # is the console "Enabled" toggle. It cannot be flipped after the fact with
    # PATCH (the app endpoint returns 405), so we set it in the create body. Only
    # type/entitlements/scopes are rejected from this body — applicationState is a
    # native top-level app field. The data.http read-back below has a
    # postcondition that fails the apply loudly if the app still comes up disabled.
    applicationState = true
    providers = {
      sso = { userOptions = "oidc" }
      oidc = {
        applicationUrl = local.boundary_addr
        properties = {
          grantTypes                = local.oidc_grant_types
          redirectUris              = [local.boundary_redirect_uri]
          idTokenSigningAlg         = "RS256"
          doNotGenerateClientSecret = "false"
          consentType               = "never_prompt"
          accessTokenExpiry         = 3600
        }
        token = {
          accessTokenType   = "jwt"
          audiences         = ["secured-codespace-boundary"]
          attributeMappings = local.group_attribute_mappings
        }
      }
    }
  }

  nomad_app_body = {
    name             = "secured-codespace-nomad"
    templateId       = "998"
    applicationState = true # enabled at create; see boundary_app_body note above
    providers = {
      sso = { userOptions = "oidc" }
      oidc = {
        applicationUrl = local.nomad_addr
        properties = {
          grantTypes                = local.oidc_grant_types
          redirectUris              = local.nomad_redirect_uris
          idTokenSigningAlg         = "RS256"
          doNotGenerateClientSecret = "false"
          consentType               = "never_prompt"
          accessTokenExpiry         = 3600
        }
        token = {
          accessTokenType   = "jwt"
          audiences         = ["secured-codespace-nomad"]
          attributeMappings = local.group_attribute_mappings
        }
      }
    }
  }
}

# Verify's POST returns ONLY `{"_links":{"self":{"href":"/appaccess/v1.0/applications/<id>"}}}`
# — no top-level id and no clientId/clientSecret. So we take the id from that href
# (provider write_returns_object = true makes the provider parse the POST response
# for it) and point read/update/destroy at the href directly ({id} == the full
# href). The post-create read GET then returns the full app object, including the
# generated clientId/clientSecret read by the locals below.
resource "restapi_object" "boundary_app" {
  path = "/v1.0/applications"
  data = jsonencode(local.boundary_app_body)

  id_attribute = "_links/self/href"
  read_path    = "{id}"
  update_path  = "{id}"
  destroy_path = "{id}"
}

resource "restapi_object" "nomad_app" {
  path = "/v1.0/applications"
  data = jsonencode(local.nomad_app_body)

  id_attribute = "_links/self/href"
  read_path    = "{id}"
  update_path  = "{id}"
  destroy_path = "{id}"
}

# Grant "Automatic access for all users and groups" on each app. Verify creates
# apps with Access Type = "Select users and groups" (no one entitled), so SSO
# login fails with CSIAQ0279E until access is granted. Access Type lives in a
# SEPARATE entitlements subsystem — it is NOT on the app object and NOT a
# create-body field — reached at POST /v1.0/owner/applications/{id}/entitlements
# with birthRightAccess=true. Only POST works: PUT/PATCH on this endpoint return
# 405 (confirmed against the tenant). data.http can't issue this mutation, so
# it's a terraform_data + local-exec curl; -fsS fails the apply loudly on any
# non-2xx. triggers_replace on the app id reruns it on (re)create (ids change
# each recreate). The token is passed via env so it never lands in argv/ps.
resource "terraform_data" "boundary_entitlement" {
  triggers_replace = restapi_object.boundary_app.id

  provisioner "local-exec" {
    environment = { VERIFY_TOKEN = var.verify_access_token }
    command     = <<-CMD
      curl -fsS -X POST \
        "${local.tenant_url}/v1.0/owner/applications/${reverse(split("/", restapi_object.boundary_app.id))[0]}/entitlements" \
        -H "Authorization: Bearer $VERIFY_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"birthRightAccess": true, "requestAccess": false}' -o /dev/null
    CMD
  }
}

resource "terraform_data" "nomad_entitlement" {
  triggers_replace = restapi_object.nomad_app.id

  provisioner "local-exec" {
    environment = { VERIFY_TOKEN = var.verify_access_token }
    command     = <<-CMD
      curl -fsS -X POST \
        "${local.tenant_url}/v1.0/owner/applications/${reverse(split("/", restapi_object.nomad_app.id))[0]}/entitlements" \
        -H "Authorization: Bearer $VERIFY_TOKEN" \
        -H "Content-Type: application/json" \
        -d '{"birthRightAccess": true, "requestAccess": false}' -o /dev/null
    CMD
  }
}

# Set the two app-level fields the create body can't carry, in ONE full-object
# PUT per app:
#   - companyName: the displayed "Company Name" (under the auto-created `saml`
#     block, even for OIDC apps), blank on create.
#   - attributeMappings (top-level): the "ID token and user info" mapping that
#     emits the `groups` claim into the ID token + userinfo — the only places
#     Boundary/Nomad read claims (the providers.oidc.token copy shapes just the
#     access-token JWT, which neither relying party inspects, so groups never
#     reaches role mapping without this). Both default empty on create.
# PATCH/POST on the app endpoint are 405; a full-object PUT (GET app, inject,
# PUT it back) is the only method that works and preserves the generated
# clientId/clientSecret. Both fields go in the SAME PUT so two writers can't
# race and clobber the object. Triggers on the app id so it reruns on (re)create.
resource "terraform_data" "boundary_app_fields" {
  triggers_replace = restapi_object.boundary_app.id

  provisioner "local-exec" {
    environment = {
      VERIFY_TOKEN  = var.verify_access_token
      APP_URL       = "${local.tenant_url}/v1.0/applications/${reverse(split("/", restapi_object.boundary_app.id))[0]}"
      COMPANY_NAME  = local.company_name
      ATTR_MAPPINGS = jsonencode(local.group_attribute_mappings)
    }
    command = "python3 ${path.module}/scripts/set_app_fields.py"
  }
}

resource "terraform_data" "nomad_app_fields" {
  triggers_replace = restapi_object.nomad_app.id

  provisioner "local-exec" {
    environment = {
      VERIFY_TOKEN  = var.verify_access_token
      APP_URL       = "${local.tenant_url}/v1.0/applications/${reverse(split("/", restapi_object.nomad_app.id))[0]}"
      COMPANY_NAME  = local.company_name
      ATTR_MAPPINGS = jsonencode(local.group_attribute_mappings)
    }
    command = "python3 ${path.module}/scripts/set_app_fields.py"
  }
}

# Read each full application back so the Verify-generated client credentials are
# available in the SAME apply. The POST response is only an `_links` stub, and the
# resource's own refresh GET happens on the NEXT plan — too late for the Boundary/
# Nomad auth methods below. This authenticated GET on the app href runs after
# create and returns clientId/clientSecret (requires the bootstrap client's
# readAppConfigAndClientSecret entitlement). `id` is the href, e.g.
# /appaccess/v1.0/applications/<id>, a valid path under the tenant URL.
data "http" "boundary_app" {
  url = "${local.tenant_url}${restapi_object.boundary_app.id}"
  request_headers = {
    Authorization = "Bearer ${var.verify_access_token}"
    Accept        = "application/json"
  }

  # Guard against a silent disable: if Verify ignored applicationState=true in
  # the create body, the app exists but stays Disabled with no error. Fail the
  # apply loudly so we know to switch the enable to a post-create PUT.
  lifecycle {
    postcondition {
      condition     = try(jsondecode(self.response_body).applicationState, false) == true
      error_message = "secured-codespace-boundary was created but is still DISABLED (applicationState != true). Verify did not honor applicationState in the create body; the app must be enabled via a separate call (PATCH returns 405 — try a full-object PUT to the app href)."
    }
  }
}

data "http" "nomad_app" {
  url = "${local.tenant_url}${restapi_object.nomad_app.id}"
  request_headers = {
    Authorization = "Bearer ${var.verify_access_token}"
    Accept        = "application/json"
  }

  lifecycle {
    postcondition {
      condition     = try(jsondecode(self.response_body).applicationState, false) == true
      error_message = "secured-codespace-nomad was created but is still DISABLED (applicationState != true). Verify did not honor applicationState in the create body; the app must be enabled via a separate call (PATCH returns 405 — try a full-object PUT to the app href)."
    }
  }
}

locals {
  boundary_oidc = try(jsondecode(data.http.boundary_app.response_body).providers.oidc.properties, {})
  nomad_oidc    = try(jsondecode(data.http.nomad_app.response_body).providers.oidc.properties, {})

  boundary_client_id     = try(local.boundary_oidc.clientId, null)
  boundary_client_secret = try(local.boundary_oidc.clientSecret, null)
  nomad_client_id        = try(local.nomad_oidc.clientId, null)
  nomad_client_secret    = try(local.nomad_oidc.clientSecret, null)
}
