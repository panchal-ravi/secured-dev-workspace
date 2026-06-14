# token-exchange

Stateless service that performs the **RFC 8693 token exchange** at IBM Verify: it
takes a user's access token (the *subject*) plus the agent's Vault-signed actor
JWT (the *actor*) and returns an **on-behalf-of (OBO) access token** scoped to the
tools the agent is allowed to call.

It is one link in the agent-platform JIT identity chain:

```
Nomad workload identity JWT
  → Vault actor JWT            (identity/oidc/token/agent — minted by the agent, NOT here)
  → token-exchange  ──► IBM Verify /oauth2/token  ──►  OBO JWT (aud=mcp-tools, actor{agent_id,entity_id})
  → mcp-auth-wrapper (validates the OBO, brokers Vault JIT creds)
```

Ported from the k8s PoC (`agentic-iam-runtimesecurity/token-exchange/`) for this
Nomad platform. See `docs/specs/agent-platform/phase-1-identity-foundation.md` §3.1.

## API

### `POST /v1/identity/obo-token`

Request (all fields required, non-empty):

```json
{
  "subject_token": "<user access_token JWT from the portal login>",
  "actor_token":   "<Vault actor JWT from identity/oidc/token/agent>",
  "scope":         "records.read records.write"
}
```

`200` response:

```json
{ "access_token": "<OBO JWT>", "cached": false }
```

| Status | Condition |
|---|---|
| 401 | IBM Verify rejected the exchange (bad client creds / expired subject or actor token) |
| 403 | Authorization gate: `groups` claim missing/malformed, scope not in policy, or groups disjoint from the scope's allowed set |
| 422 | Missing/empty `subject_token`, `actor_token`, or `scope` |
| 500 | Verify exchange failed (non-401, after 3 retries) or cache failure |
| 503 | Network failure reaching IBM Verify |

Send `X-Request-ID` to correlate logs; it is echoed back when supplied.

### `GET /healthz`

`200 {"status":"ok"}` — liveness (used as the Nomad service check).

## Authorization policy

The group→scope gate is **loaded from a JSON file at startup**
(`TOKEN_EXCHANGE_SCOPE_POLICY_FILE`). Keys are scopes, values are IBM Verify
group names (compared **case-insensitively**):

```json
{
  "records.read":  ["acme-operators", "acme-admins"],
  "records.write": ["acme-admins"]
}
```

Fail-closed by construction: a missing or malformed policy file **aborts startup**,
and an empty policy denies every exchange (403). The file is rendered into the
container by the Nomad `template` stanza from a Terraform variable.

## Configuration (env prefix `TOKEN_EXCHANGE_`)

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `TOKEN_EXCHANGE_VERIFY_BASE_URL` | yes | — | Verify base URL; token endpoint is `{base}/oauth2/token` |
| `TOKEN_EXCHANGE_OBO_CLIENT_ID` | yes | — | `agent-token-exchange` app client id |
| `TOKEN_EXCHANGE_OBO_CLIENT_SECRET` | yes | — | confidential client secret (fail-fast if empty) — from Vault KV via Nomad `template` |
| `TOKEN_EXCHANGE_SCOPE_POLICY_FILE` | yes | — | path to the scope→groups JSON (e.g. `/local/scope-policy.json`) |
| `TOKEN_EXCHANGE_CACHE_TTL` | no | `3600` | cache eviction window (s) |
| `TOKEN_EXCHANGE_CACHE_MAXSIZE` | no | `1024` | max cached tokens |
| `TOKEN_EXCHANGE_LOG_LEVEL` | no | `INFO` | log level |

## Why no `hvac` / Vault client

The PoC carried `hvac` for its `/v1/identity/token` route, which exchanged a Vault
token for a Vault-signed identity JWT. **That route is dropped here**: on this
platform the agent obtains its actor JWT directly from Vault
(`identity/oidc/token/agent`, via its Nomad workload-identity login), so
token-exchange makes **zero Vault API calls**. Its one secret (the Verify client
secret) is delivered by the Nomad `template` stanza from Vault KV, not by an
in-process Vault client. Result: no `hvac` dependency, no Vault token/policy held
by this service (consistent with "no standing credentials"), smaller image, and a
smaller dependency/CVE surface.

## Develop & test

```bash
uv sync
uv run pytest -q          # 70 tests
```

For local runs, put the env vars in a `.env` file (gitignored) and point
`TOKEN_EXCHANGE_SCOPE_POLICY_FILE` at a local JSON policy, then:

```bash
uv run uvicorn api.main:app --port 4460
```

## Build & deploy

Container image (uv build, non-root `tokenx` uid 10001, `uvicorn :4460`, curl
healthcheck on `/healthz`):

```bash
./scripts/build-image.sh                                   # → panchalravi/token-exchange:poc (amd64, pushed)
TOKEN_EXCHANGE_IMAGE=panchalravi/token-exchange:v1 ./scripts/build-image.sh
```

Then set `var.token_exchange_image` and `enable_token_exchange = true` and apply
`terraform/infra/token-exchange.tf` (task P1.3). The job reads the Verify client
secret from Vault KV `secret/infra/token-exchange` via the Nomad `template` stanza
and renders the scope policy to `/local/scope-policy.json`.

Quick local smoke (native arch):

```bash
docker build -t token-exchange:local .
echo '{"records.read":["acme-admins"]}' > /tmp/scope-policy.json
docker run --rm -p 4460:4460 \
  -e TOKEN_EXCHANGE_OBO_CLIENT_SECRET=dummy \
  -e TOKEN_EXCHANGE_OBO_CLIENT_ID=dummy \
  -e TOKEN_EXCHANGE_VERIFY_BASE_URL=https://example.verify.ibm.com \
  -e TOKEN_EXCHANGE_SCOPE_POLICY_FILE=/local/scope-policy.json \
  -v /tmp/scope-policy.json:/local/scope-policy.json:ro \
  token-exchange:local
# curl localhost:4460/healthz → {"status":"ok"}
```

## Layout

```
api/          FastAPI app (main: middleware + lifespan policy load; routes: obo-token + healthz)
verify/       authorization (file-loaded scope gate), obo_broker (cache+retry), verify_client (RFC 8693)
broker/       cache.py (SHA256-keyed TTL cache; subject_token + cache_slot)
config/       settings.py (pydantic-settings, TOKEN_EXCHANGE_ prefix)
models/       OBO request/response schemas
exceptions/   typed error hierarchy
app_logging/  structlog JSON logging (request-id contextvars)
tests/        pytest suites (routes, authorization, cache, broker, verify_client, settings, logging)
Dockerfile    uv build → non-root uvicorn :4460 (P1.2)
scripts/      build-image.sh (buildx → registry)
```

Terraform deploy (`terraform/infra/token-exchange.tf`) lands in task P1.3.
