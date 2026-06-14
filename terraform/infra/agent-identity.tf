# Agent-platform identity foundation (Phase 1, spec §3.2 / overview §4.1).
#
# Codifies the Vault half of the JIT identity chain that was hand-proven in the
# Phase 0 spikes (findings §2 + §3): a Nomad workload-identity JWT logs in to
# jwt-nomad, the resulting entity mints a 1h "actor" identity-OIDC JWT, and that
# actor JWT is the RFC 8693 actor_token presented to IBM Verify's
# agent-token-exchange app. All resources are ADDITIVE — nothing here touches the
# base instance, the existing jwt-nomad mount config, or any developer-flow path.
# The vault provider (providers.tf) talks to the already-running Vault over the
# NLB with the root token.

# ---------------------------------------------------------------------------
# Issuer (resolves Phase 0 findings §3 OPEN design point #1 — VERIFIED in Spike 2).
#
# Vault's default identity-OIDC issuer is derived from api_addr, which on this
# platform is https://127.0.0.1:8200/... . IBM Verify validates the actor token's
# `iss` claim and REJECTED that default (error CSIAQ5204E) until the issuer was
# pointed at the NLB address Verify can reach. The Spike 2 exchange only returned
# 200 after setting this. Verify DOES reach Vault's JWKS at
# <issuer>/.well-known/keys over the NLB (signature validation passed) — so the
# spec's earlier "Vault is not internet-reachable" assumption does not hold here.
#
# This is a GLOBAL identity-OIDC config (singleton). Safe because the agent
# identity below is the only consumer of identity/oidc in this Vault — SSO uses
# Verify OIDC (external) and WIF uses the jwt-nomad mount, neither of which reads
# identity/oidc/config.
#
# NOTE: this endpoint requires issuer = scheme+host+(port) ONLY — Vault appends
# `/v1/identity/oidc` itself to form the token `iss`. vault_addr is already
# `https://<nlb>:8200`, so the minted actor JWT carries
# `iss=https://<nlb>:8200/v1/identity/oidc`, which is what Verify validates.
resource "vault_identity_oidc" "server" {
  issuer = module.secured_codespace.vault_addr
}

resource "vault_identity_oidc_key" "agent_identity" {
  name             = "agent-identity"
  algorithm        = "RS256"
  rotation_period  = 86400 # 24h signing-key rotation
  verification_ttl = 86400 # old key verifiable for one more period

  # The issuer must exist before keys are minted under it.
  depends_on = [vault_identity_oidc.server]
}

# Accessor of the jwt-nomad mount, needed to address the workload-identity ALIAS
# in the actor-JWT template below. Not hardcoded — it changes on a platform
# rebuild.
data "vault_auth_backend" "nomad" {
  path = module.nomad_vault_wif.backend_path
}

# Actor JWT shape. agent_id = the jwt-nomad entity ALIAS name (= the /nomad_job_id
# user_claim, i.e. the agent's Nomad job id), entity_id = the Vault entity id —
# exactly the claims the Verify agent-token-exchange app's introspect rule maps
# into the OBO `actor` claim (verified live: OBO carried actor.{agent_id, entity_id}).
# NB: must use entity.aliases.<accessor>.name, NOT entity.name — auto-created
# entities get an opaque name (entity_xxxx); only the alias carries the job id.
resource "vault_identity_oidc_role" "agent" {
  name = "agent"
  key  = vault_identity_oidc_key.agent_identity.name
  ttl  = 3600 # 1h actor JWT (agent refreshes on a timer, Phase 3)

  # Vault 1.20 identity templating substitutes string {{…}} directives WITH their
  # own surrounding quotes, so the directives must NOT be wrapped in quotes in the
  # template (quoting yields `""value""` → 400 "invalid character '\"'"; verified
  # empirically against this build). jsonencode is kept for correct escaping of the
  # static claims, then the two templated fields' quoted sentinels are unquoted.
  template = replace(replace(jsonencode({
    org           = var.agent_identity_claims.org
    bu            = var.agent_identity_claims.bu
    department    = var.agent_identity_claims.department
    service_group = var.agent_identity_claims.service_group
    entity_id     = "__ENTITY_ID__"
    agent_id      = "__AGENT_ID__"
  }), "\"__ENTITY_ID__\"", "{{identity.entity.id}}"), "\"__AGENT_ID__\"", "{{identity.entity.aliases.${data.vault_auth_backend.nomad.accessor}.name}}")
}

resource "vault_identity_oidc_key_allowed_client_id" "agent" {
  key_name          = vault_identity_oidc_key.agent_identity.name
  allowed_client_id = vault_identity_oidc_role.agent.client_id
}

# JWT auth mount trusting Verify-issued OBO tokens. Vault enforces audiences per
# ROLE (bound_audiences), not per mount — overview §4.1's 'bound to aud
# ["mcp-tools"]' is implemented on every role attached to this mount (Phase 2:
# <p>-records-read|write). The mount pins the issuer + JWKS source.
#
# Phase 2 reconciliation (Spike 2 evidence): the OBO token's `iss` is
# `<tenant>/oauth2`, which DIFFERS from the login/portal issuer
# (`<tenant>/oidc/endpoint/default`) used here for JWKS discovery. When Phase 2
# attaches the first role, set bound_issuer = "<tenant>/oauth2" (the OBO iss) and,
# if Verify does not serve discovery at that path, decouple via an explicit
# jwks_url = "<tenant>/oidc/endpoint/default/jwks". No role validates OBO tokens
# in Phase 1, so the discovery-only config below is sufficient for the gate.
resource "vault_jwt_auth_backend" "obo" {
  path               = "jwt-obo"
  type               = "jwt"
  oidc_discovery_url = var.portal_oidc_issuer # Verify issuer (serves .well-known + JWKS)
  bound_issuer       = var.portal_oidc_issuer
}

# Phase-gate scaffolding (kept for regression): policy + jwt-nomad role letting an
# infra batch job named obo-smoke mint the actor JWT. Phase 3's per-project
# <p>-agent roles reuse vault_policy.agent_oidc_token_read.
resource "vault_policy" "agent_oidc_token_read" {
  name   = "agent-oidc-token-read"
  policy = <<-HCL
    path "identity/oidc/token/${vault_identity_oidc_role.agent.name}" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_jwt_auth_backend_role" "infra_agent_smoke" {
  backend                 = module.nomad_vault_wif.backend_path
  role_name               = "infra-agent-smoke"
  role_type               = "jwt"
  bound_audiences         = ["vault.io"]
  user_claim              = "/nomad_job_id"
  user_claim_json_pointer = true
  bound_claims            = { nomad_namespace = "infra", nomad_job_id = "obo-smoke" }
  token_policies          = [vault_policy.agent_oidc_token_read.name]
  token_ttl               = 900
  token_max_ttl           = 1800
  token_type              = "service"
}
