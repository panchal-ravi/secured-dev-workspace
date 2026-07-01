#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Per-project MCP gateway orchestration, invoked by terraform/project/mcp-gateway.tf
# (terraform_data create/destroy provisioners). All inputs arrive via the
# ENVIRONMENT (never the command line) so secrets are not logged. Verbs:
#
#   provision   register demo-db-mcp as a peer in the shared ContextForge gateway,
#               discover its tools, compose a per-project virtual server, create a
#               per-project server-scoped client token, and write {url,token} to
#               Vault KV at secret/projects/mcp (read by the workspace).
#   deprovision revoke the client token, delete the virtual server, the peer
#               registration, and the KV path.
#
# Auth is JWT-only (Basic auth is disabled on the gateway). The ADMIN call token is
# an HS256 JWT over JWT_SECRET_KEY for the platform-admin identity — minted here
# with openssl (only the bootstrap admin email is accepted as a bare JWT). The
# CLIENT token is a real gateway API token (POST /tokens, DB-backed, scoped to the
# project's virtual server), since hand-minted JWTs for arbitrary users are
# rejected. To mint the admin JWT with the bundled utility instead, e.g.:
#   alloc=$(curl -sk -H "X-Nomad-Token: $NOMAD_TOKEN" \
#     "$NOMAD_ADDR/v1/job/mcp-gateway/allocations?namespace=$INFRA_NS" \
#     | jq -r '[.[]|select(.ClientStatus=="running")][0].ID')
#   NOMAD_SKIP_VERIFY=true nomad alloc exec -namespace "$INFRA_NS" -task gateway "$alloc" \
#     python3 -m mcpgateway.utils.create_jwt_token --username "$U" --exp "$E" --secret "$JWT_SECRET"
#
# Field names below were confirmed against the live gateway's OpenAPI schema
# (ContextForge 1.0.2): GatewayRead/ToolRead/ServerRead expose .id/.name/.gatewayId;
# POST /gateways = GatewayCreate{name,url} (transport defaults to SSE); POST /servers
# = {server: ServerCreate{name,description,associated_tools:[ids]}}.
# ---------------------------------------------------------------------------
set -euo pipefail

VERB="${1:?usage: mcp-provision.sh provision|deprovision}"

: "${GW_URL:?}" "${PROJECT:?}" "${PEER_NAME:?}" "${VS_NAME:?}" "${ADMIN_EMAIL:?}" "${JWT_SECRET:?}"
: "${VAULT_ADDR:?}" "${VAULT_TOKEN:?}" "${KV_MOUNT:?}" "${KV_NAME:?}" "${VAULT_NAMESPACE:?}"

b64url() { openssl base64 -e -A | tr '+/' '-_' | tr -d '='; }

# Mint an HS256 JWT with the claims ContextForge actually verifies (confirmed at
# Gate 1 against the live gateway — a token missing iss/aud is rejected 401):
#   sub/username, iss (JWT_ISSUER default "mcpgateway"), aud (JWT_AUDIENCE default
#   "mcpgateway-api"), iat, exp (expiration is required), jti (revocation id).
# The gateway uses the defaults — we do not override JWT_ISSUER/JWT_AUDIENCE in the
# infra job — so they are hardcoded here to match.
mint_jwt() {
  local username="$1" exp_min="$2" now exp jti header payload sig
  now="$(date +%s)"
  exp="$(( now + exp_min * 60 ))"
  jti="$(openssl rand -hex 16)"
  header="$(printf '{"alg":"HS256","typ":"JWT"}' | b64url)"
  payload="$(printf '{"sub":"%s","username":"%s","iss":"mcpgateway","aud":"mcpgateway-api","iat":%s,"exp":%s,"jti":"%s"}' \
    "$username" "$username" "$now" "$exp" "$jti" | b64url)"
  sig="$(printf '%s.%s' "$header" "$payload" | openssl dgst -sha256 -hmac "$JWT_SECRET" -binary | b64url)"
  printf '%s.%s.%s' "$header" "$payload" "$sig"
}

# Authenticated gateway admin call: gw METHOD PATH [JSON-BODY].
gw() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$GW_URL$path" \
      -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" -d "$body"
  else
    curl -fsS -X "$method" "$GW_URL$path" -H "Authorization: Bearer $ADMIN_JWT"
  fi
}

# Revoke every live API token belonging to this project's client (best-effort).
# ContextForge DELETE is a SOFT delete (is_active=false) that PERMANENTLY reserves
# the token's exact name — re-creating that name 409s. So we mint a uniquely-named
# token per run (CLIENT_USERNAME-<rand>, see provision step 4) and clean up here by
# PREFIX, deleting only still-active tokens (deleting an already-inactive one 409s).
# GET /tokens lists the caller's active tokens (inactive ones are hidden by default),
# which is exactly the set to revoke when superseding the project's credential.
revoke_client_tokens() {
  local id ids
  ids="$(gw GET "/tokens?limit=100" 2>/dev/null \
    | jq -r --arg p "${CLIENT_USERNAME}-" '.tokens[]? | select(.name|startswith($p)) | .id')" || return 0
  for id in $ids; do
    gw DELETE "/tokens/$id" >/dev/null 2>&1 && echo "revoked prior client token ($id)" || true
  done
}

provision() {
  : "${GW_PRIVATE_URL:?}" "${PEER_URL:?}" "${CLIENT_USERNAME:?}" "${CLIENT_EXP_DAYS:?}"
  ADMIN_JWT="$(mint_jwt "$ADMIN_EMAIL" 60)"

  # 1) Register the peer MCP server (idempotent — reuse if the name already exists).
  local peer_id
  peer_id="$(gw GET /gateways | jq -r --arg n "$PEER_NAME" 'first(.[]? | select(.name==$n) | .id) // empty')"
  if [ -z "$peer_id" ]; then
    echo "registering peer $PEER_NAME -> $PEER_URL"
    peer_id="$(gw POST /gateways "$(jq -n --arg n "$PEER_NAME" --arg u "$PEER_URL" '{name:$n,url:$u}')" | jq -r '.id')"
  fi
  [ -n "$peer_id" ] || { echo "ERROR: no peer id" >&2; exit 1; }

  # 2) Wait for the gateway to discover the peer's tools, then collect their ids.
  local tool_ids="[]" n=0
  for _ in $(seq 1 30); do
    tool_ids="$(gw GET /tools | jq -c --arg g "$peer_id" '[.[]? | select((.gatewayId==$g) or (.gateway_id==$g)) | .id]')"
    n="$(printf '%s' "$tool_ids" | jq 'length')"
    [ "$n" -gt 0 ] && break
    sleep 2
  done
  [ "$n" -gt 0 ] || { echo "ERROR: no tools discovered for peer $PEER_NAME" >&2; exit 1; }
  echo "discovered $n tool(s) for $PEER_NAME"

  # 3) Compose the per-project virtual server (idempotent).
  local vs_id
  vs_id="$(gw GET /servers | jq -r --arg n "$VS_NAME" 'first(.[]? | select(.name==$n) | .id) // empty')"
  if [ -z "$vs_id" ]; then
    local body
    body="$(jq -n --arg n "$VS_NAME" --argjson t "$tool_ids" \
      '{server:{name:$n,description:("read-only demo-db tools for project " + env.PROJECT),associated_tools:$t}}')"
    vs_id="$(gw POST /servers "$body" | jq -r '.id // .server.id')"
  fi
  [ -n "$vs_id" ] || { echo "ERROR: no virtual server id" >&2; exit 1; }
  echo "virtual server $VS_NAME = $vs_id"

  # 4) Create the per-project CLIENT credential and store {url,token} in Vault KV.
  # A hand-minted JWT only authenticates the bootstrap admin identity (any other
  # sub fails JWT validation and is rejected 401). Client identities must be real
  # gateway API tokens (DB-backed, looked up by hash). We create one SCOPED to this
  # virtual server (scope.server_id, with a finite expires_in_days — both required
  # for the scope to bind) so the token reaches ONLY this project's virtual MCP
  # server: 200 on /servers/<vs>/..., 403 on every other server/admin endpoint
  # (confirmed against the live gateway). The name carries a per-run suffix because
  # a deleted token's exact name stays reserved (see revoke_client_tokens); revoke
  # any prior live client token first so a re-apply supersedes (not duplicates) it.
  revoke_client_tokens
  local run_id client_name client_token url
  run_id="$(openssl rand -hex 4)"
  client_name="${CLIENT_USERNAME}-${run_id}"
  client_token="$(gw POST /tokens "$(jq -n --arg n "$client_name" --argjson d "$CLIENT_EXP_DAYS" --arg s "$vs_id" \
    '{name:$n,expires_in_days:$d,scope:{server_id:$s}}')" | jq -r '.access_token')"
  [ -n "$client_token" ] && [ "$client_token" != "null" ] || { echo "ERROR: no client token returned" >&2; exit 1; }
  url="$GW_PRIVATE_URL/servers/$vs_id/sse"
  curl -fsSk -X POST "$VAULT_ADDR/v1/$KV_MOUNT/data/$KV_NAME" \
    -H "X-Vault-Token: $VAULT_TOKEN" \
    -H "X-Vault-Namespace: $VAULT_NAMESPACE" \
    -d "$(jq -n --arg u "$url" --arg t "$client_token" '{data:{url:$u,token:$t}}')" >/dev/null
  echo "wrote $KV_MOUNT/$KV_NAME (url + scoped client token $client_name)"
}

# Best-effort cleanup — never fail a terraform destroy on a gateway hiccup.
deprovision() {
  ADMIN_JWT="$(mint_jwt "$ADMIN_EMAIL" 60)" || true
  set +e
  revoke_client_tokens
  local vs_id peer_id
  vs_id="$(gw GET /servers 2>/dev/null | jq -r --arg n "$VS_NAME" 'first(.[]? | select(.name==$n) | .id) // empty')"
  [ -n "$vs_id" ] && { gw DELETE "/servers/$vs_id" >/dev/null 2>&1; echo "deleted virtual server $VS_NAME"; }
  peer_id="$(gw GET /gateways 2>/dev/null | jq -r --arg n "$PEER_NAME" 'first(.[]? | select(.name==$n) | .id) // empty')"
  [ -n "$peer_id" ] && { gw DELETE "/gateways/$peer_id" >/dev/null 2>&1; echo "deleted peer $PEER_NAME"; }
  curl -sk -X DELETE "$VAULT_ADDR/v1/$KV_MOUNT/metadata/$KV_NAME" \
    -H "X-Vault-Token: $VAULT_TOKEN" \
    -H "X-Vault-Namespace: $VAULT_NAMESPACE" >/dev/null 2>&1
  echo "removed $KV_MOUNT/$KV_NAME"
  set -e
}

case "$VERB" in
  provision)   provision ;;
  deprovision) deprovision ;;
  *) echo "unknown verb: $VERB" >&2; exit 2 ;;
esac
