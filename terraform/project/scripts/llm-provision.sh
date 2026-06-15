#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# Per-project LiteLLM gateway orchestration, invoked by terraform/project/
# llm-gateway.tf (terraform_data create/destroy provisioners). All inputs arrive via
# the ENVIRONMENT (never the command line) so secrets are not logged. Verbs:
#
#   provision   mint a per-project virtual key in the shared LiteLLM gateway, scoped
#               to the project's allowed models + a budget + an rpm limit, and write
#               {base_url, virtual_key} to Vault KV at secret/projects/<project>/llm
#               (read by the workspace; Claude Code presents the key to the gateway).
#   deprovision revoke the key (by alias) and delete the KV path.
#
# Auth is the gateway MASTER key (Authorization: Bearer). The key is identified by a
# stable per-project alias (KEY_ALIAS=llm-<project>) so a re-apply supersedes (not
# duplicates) it: delete-by-alias first, then generate. /key/delete is a hard delete
# in LiteLLM, so the alias frees up for re-creation.
# ---------------------------------------------------------------------------
set -euo pipefail

VERB="${1:?usage: llm-provision.sh provision|deprovision}"

: "${GW_ADMIN_URL:?}" "${PROJECT:?}" "${KEY_ALIAS:?}" "${MASTER_KEY:?}"
: "${VAULT_ADDR:?}" "${VAULT_TOKEN:?}" "${KV_MOUNT:?}" "${KV_NAME:?}" "${VAULT_NAMESPACE:?}"

# Authenticated gateway admin call: gw METHOD PATH [JSON-BODY].
gw() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$GW_ADMIN_URL$path" \
      -H "Authorization: Bearer $MASTER_KEY" -H "Content-Type: application/json" -d "$body"
  else
    curl -fsS -X "$method" "$GW_ADMIN_URL$path" -H "Authorization: Bearer $MASTER_KEY"
  fi
}

# Revoke this project's virtual key by alias (best-effort — a missing key is fine).
revoke_key() {
  gw POST /key/delete "$(jq -n --arg a "$KEY_ALIAS" '{key_aliases:[$a]}')" >/dev/null 2>&1 \
    && echo "revoked prior virtual key ($KEY_ALIAS)" || true
}

provision() {
  : "${GW_BASE_URL:?}" "${MODELS:?}" "${MAX_BUDGET:?}" "${RPM_LIMIT:?}"

  # Supersede any prior key for this project, then mint a fresh scoped one.
  revoke_key
  local models_json body virtual_key
  models_json="$(printf '%s' "$MODELS" | jq -R 'split(",")')"
  body="$(jq -n \
    --arg a "$KEY_ALIAS" \
    --argjson m "$models_json" \
    --argjson b "$MAX_BUDGET" \
    --argjson r "$RPM_LIMIT" \
    --arg p "$PROJECT" \
    '{key_alias:$a, models:$m, max_budget:$b, rpm_limit:$r, metadata:{project:$p}}')"
  virtual_key="$(gw POST /key/generate "$body" | jq -r '.key')"
  [ -n "$virtual_key" ] && [ "$virtual_key" != "null" ] || { echo "ERROR: no virtual key returned" >&2; exit 1; }
  echo "minted virtual key $KEY_ALIAS (models: $MODELS, budget: \$$MAX_BUDGET, rpm: $RPM_LIMIT)"

  # Store {base_url, virtual_key} for the workspace. The workspace reads virtual_key
  # to /secrets/llm-key over WIF; base_url is also baked into managed-settings.json
  # via the kv.tf llm_base_url placeholder (stored here too for completeness).
  curl -fsSk -X POST "$VAULT_ADDR/v1/$KV_MOUNT/data/$KV_NAME" \
    -H "X-Vault-Token: $VAULT_TOKEN" \
    -H "X-Vault-Namespace: $VAULT_NAMESPACE" \
    -d "$(jq -n --arg u "$GW_BASE_URL" --arg k "$virtual_key" '{data:{base_url:$u,virtual_key:$k}}')" >/dev/null
  echo "wrote $KV_MOUNT/$KV_NAME (base_url + virtual key)"
}

# Best-effort cleanup — never fail a terraform destroy on a gateway hiccup.
deprovision() {
  set +e
  revoke_key
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
