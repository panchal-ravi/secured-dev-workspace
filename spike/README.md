# Phase 0 spikes — THROWAWAY

Everything under `spike/` is throwaway de-risking scaffolding for **Phase 0**
(`docs/specs/agent-platform/phase-0-spikes.md`). It is **not** production code and
is removed at phase close per the spec's Deliverables table. Evidence (redacted)
lands in `docs/specs/agent-platform/phase-0-findings.md`; **no token/secret material
is committed here** — the jobspecs carry `REPLACE_*` placeholders the operator fills
at run time (and clears from shell history afterward).

## Contents

| Path | Spike | What it does |
|---|---|---|
| `header-echo/server.py`, `client.py`, `spike-header-echo.nomad.hcl` | 1 | MCP server echoing request headers — proves ContextForge `X-Upstream-Authorization` passthrough |
| `agent-wi/agent-spike-wi.nomad.hcl` | 3 | `agent-`prefixed batch job that mints a Nomad workload-identity JWT for the Vault→actor-JWT chain |
| `litellm/spike-litellm-pg.nomad.hcl`, `spike-litellm.nomad.hcl` | 4 | Scratch LiteLLM (DB mode) + Postgres — model CRUD + per-instance key pattern, real gateway untouched |

(Spike 2 is IBM Verify **console** work — operator-driven in a browser; recipe in
`docs/IBM_VERIFY_AGENT_APPS.md`. Spike 5's artifacts are **kept** and live in
`terraform/infra/` — `agent-nodes.tf` + `ami/agent_image/`, not here.)

## Run order

1. **Spike 3** first (its actor JWT feeds spike 2 step 5b), or in parallel.
2. **Spike 1** (decision gate) — register peer, compose VS, mint scoped token, call `echo_headers` with the extra header.
3. **Spike 4** — scratch ns `spike`, model CRUD + ephemeral keys.
4. **Spike 2** — Verify console + RFC 8693 curl (needs spike 3's actor JWT + a portal user access_token).
5. **Spike 5** — `terraform apply` the agent nodes (see `phase-0-tasks.md` P0.5.*).

Each spike's exact command sequence + pass/fail + rollback is in `phase-0-spikes.md`.
Cleanup (`rm -rf spike/…`, purge Nomad jobs, revoke gateway token, delete Vault
throwaways, remove temp Verify redirect URI) is mandatory before the phase gate.
