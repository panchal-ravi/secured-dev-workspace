#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Bootstrap the Developer Portal's LiteLLM "portal-admin" key. The portal's admin
# plane manages models (POST /model/new etc.) with a DEDICATED non-master,
# proxy-admin key — never the gateway master key. This mints (or re-mints) that key
# via the master key and stores it at secret/infra/llm-gateway.portal_admin_key,
# which the portal reads over WIF. Invoked by terraform/infra/platform-admin.tf
# (terraform_data local-exec); all inputs arrive via the ENVIRONMENT so the master
# key and Vault token are never on the command line / in logs.
#
# Idempotent: a stable proxy-admin user (portal-admin) is created once; re-applies
# mint a fresh key for it. Requires the gateway reachable (operator /32 via the NLB)
# and jq. Manual fallback if this can't run: create a proxy_admin key in the LiteLLM
# Admin UI and `vault kv patch secret/infra/llm-gateway portal_admin_key=<key>`.
# ---------------------------------------------------------------------------
set -euo pipefail

: "${GW_URL:?}" "${MASTER_KEY:?}" "${VAULT_ADDR:?}" "${VAULT_TOKEN:?}" "${KV_MOUNT:?}" "${KV_NAME:?}"

# Wait for the gateway to answer its liveness probe before minting.
for _ in $(seq 1 30); do
  curl -fsS "$GW_URL/health/liveliness" >/dev/null 2>&1 && break || sleep 2
done

gw() {
  curl -fsS -X POST "$GW_URL$1" \
    -H "Authorization: Bearer $MASTER_KEY" -H "Content-Type: application/json" -d "$2"
}

# Create the proxy-admin user with an initial key (auto_create_key). If the user
# already exists, /user/new errors; fall back to minting a fresh key for it.
key="$(gw /user/new '{"user_id":"portal-admin","user_role":"proxy_admin","auto_create_key":true}' 2>/dev/null \
  | jq -r '.key // empty')" || true

if [ -z "$key" ] || [ "$key" = "null" ]; then
  key="$(gw /key/generate "$(jq -n '{user_id:"portal-admin",key_alias:("portal-admin-" + (now|floor|tostring))}')" \
    | jq -r '.key // empty')"
fi
[ -n "$key" ] && [ "$key" != "null" ] || { echo "ERROR: could not mint portal-admin key" >&2; exit 1; }

# Patch only the portal_admin_key subkey, preserving the other llm-gateway fields.
curl -fsSk -X PATCH "$VAULT_ADDR/v1/$KV_MOUNT/data/$KV_NAME" \
  -H "X-Vault-Token: $VAULT_TOKEN" \
  -H "Content-Type: application/merge-patch+json" \
  -d "$(jq -n --arg k "$key" '{data:{portal_admin_key:$k}}')" >/dev/null

echo "stored portal_admin_key in $KV_MOUNT/$KV_NAME"
