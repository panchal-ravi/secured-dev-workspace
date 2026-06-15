# Project tier — one project per onboarding (`terraform/project/`)

The **project tier** of the three-tier model (`terraform/{infra,project,workspace}`). A **flat root** (no child module) applied **once per project**, using a separate **Terraform workspace** for each. It reads the **platform** root's outputs via `terraform_remote_state` (`../infra/terraform.tfstate`) — **no tokens or addresses go in tfvars**; just pick a project name, the IBM Verify developers group for it, and the project's GitHub App.

See [`../README.md`](../README.md) for the three-tier overview and the end-to-end flow.

## What it creates

Applied **once per project** (one `terraform workspace` per project). Creates, scoped to the project:

- A **Boundary project scope** under the org, a **Nomad namespace**, and a **Vault Enterprise namespace** (all = project name). The Vault namespace is the **hard isolation boundary**: every project Vault object below lives inside it, and a token minted in `<project>` can read only `<project>` paths — isolation is **Vault-enforced**, not dependent on path-prefix policy correctness.
- A namespace-scoped **Vault SSH CA** (`ssh/<project>` mount + `dev-workspace` signing role with `permit-pty`/`permit-port-forwarding`, 5m/10m TTL, `key_id` = the developer's email), inside the project's Vault namespace.
- The **Boundary Vault credential store** (authenticated with a dedicated least-privilege periodic token, not root) + the **SSH credential library** every workspace target in the project injects.
- A **per-namespace `jwt-nomad` auth backend** (auth methods don't cross Vault namespaces, so each project gets its own, trusting Nomad's JWKS) + a **WIF role** on it + a read-only policy exposing only this project's CA public key, and a namespace-scoped **Nomad ACL policy + binding rule** (defense-in-depth). Each workspace/service Nomad job sets `vault { namespace = "<project>" }` so its workload-identity login targets the namespace backend.
- A **GitHub App token broker** (`github/<project>` mount + config + a pre-scoped `dev-workspace` permission set, plus a WIF policy letting the workspace read a `contents:write` token from it) — the git push credential for every workspace in the project; no static PAT anywhere.
- A throwaway **demo Postgres** (`demo-db`, seeded on every (re)start, node-local static port, **not** on the NLB) plus a **per-project Vault database secrets engine** (`database/<project>` mount + a `demo-db` connection + a SELECT-only `dev-workspace-ro` role) that mints **dynamic, auto-rotating read-only** DB credentials — no static DB password.
- The project's **MCP server** (`demo-db-mcp`) — a long-lived `postgres-mcp` SSE Nomad service that connects to `demo-db` with the dynamic read-only role over WIF (replacing the old per-workspace stdio Postgres MCP that used to be baked into every image).
- The project's **virtual MCP server** in the shared **ContextForge** gateway: a `terraform_data` / `scripts/mcp-provision.sh` orchestration registers `demo-db-mcp` as a gateway **peer**, composes a per-project **virtual MCP server**, mints a **scoped client token**, and writes the virtual-server URL + token to Vault KV (`secret/projects/<project>/mcp`) for the workspace to read over WIF and point Claude's remote MCP at. (The gateway itself lives in the platform tier.)
- The project's **LiteLLM virtual key** in the shared **LiteLLM AI gateway**: a `terraform_data` / `scripts/llm-provision.sh` orchestration mints ONE scoped **virtual key** (allowed models + `max_budget` + `rpm_limit`, alias `llm-<project>`) via the gateway master key and writes `{base_url, virtual_key}` to Vault KV (`secret/projects/<project>/llm`) for the workspace to read over WIF. Claude Code points `ANTHROPIC_BASE_URL` at the gateway and authenticates with this per-project key — **never the real provider key**. (The gateway itself lives in the platform tier.)
- The project's **Nomad job templates** ("flavors"), written as raw HCL to Vault KV at `secret/projects/<project>/job-templates/<name>`, **each with its OWN pinned image AND git repo** (from `workspace_templates`), plus an optional `node_pool` (`"gpu"` for the GPU flavor, `"microvm"` for the Kata-isolated microVM flavor). At publish the project-static values (namespace, image, repo, WIF role, SSH CA path, GitHub/MCP/LLM paths) are baked in, leaving only the per-workspace placeholders — so both the developer tier and the Developer Portal render the **same** template and a developer never selects an image or repo.

The outputs (`project_scope_id`, `namespace`, `credential_library_id`, `ssh_ca_path`, `wif_role`, `github_token_path`, `mcp_kv_path`, `llm_kv_path`, `job_template_names`, `job_template_node_pools`, …) feed the developer tier; a parallel `portal-descriptor` (with the same flavors, their picker metadata, and node pools) is published to Vault KV for the Developer Portal.

## Prerequisite: a GitHub App (manual, one-time per project)

Like the IBM Verify setup on the platform tier, each project needs a GitHub App before the first apply:

1. Create a **GitHub App** (org or personal) with repository permission **Contents: Read and write** (GitHub auto-adds **Metadata: Read-only**). No webhook needed.
2. **Install** it on the account/org that owns the project's repos, granting it those repos.
3. Collect three values for the tfvars: the **App ID**, the **Installation ID** (from the installation URL `…/installations/<id>`), and a generated **private key** `.pem`. If the key is PKCS#8 (`-----BEGIN PRIVATE KEY-----`), convert it to the PKCS#1 form the plugin expects: `openssl rsa -in key.pem -out key.pkcs1.pem`.

## Prerequisite: build the workspace images (one per flavor)

The project **owns the workspace toolchain images** — each job template ("flavor") under `templates/` is pinned to an image the project team builds and pushes. Build a **public**, multi-arch manifest (`linux/amd64` + `linux/arm64`) so it runs on the EC2 node (amd64) regardless of build host; Nomad pulls it at launch (`force_pull = true`). One image per template, from `images/<name>/`:

```bash
docker login                                            # authenticate to Docker Hub
docker buildx create --name multiarch --use --bootstrap # docker-container driver (once)
docker buildx build --platform linux/amd64,linux/arm64 \
  -t <dockerhub-user>/dev-workspace:poc --push images/dev-workspace
```

The **GPU flavor** image builds from `images/gpu-workspace/` (a CUDA-devel base with full dev-workspace parity — `nvidia-smi`/`nvcc` plus Claude/DB-MCP/git). It only runs on the amd64 GPU node, so a single-arch `linux/amd64` build is enough; it still pulls at launch.

The **microVM flavor** (`microvm-workspace`) needs **no separate image** — it runs the **same `dev-workspace` image**, just placed on the `microvm` pool with `runtime = "kata"` so it boots inside a Kata Containers microVM (its own guest kernel behind a KVM boundary). Isolation is added at the runtime/node layer, not in the image, so there is nothing extra to build.

Then add an entry per template to `workspace_templates` (see tfvars below) mapping the template name to its pushed `image`, its `git_repo_url`, a picker `label`/`description`, and an optional `node_pool` (`"gpu"` for the GPU flavor — places the workspace on the GPU node and requests an `nvidia/gpu` device; `"microvm"` for the microVM flavor — places it on the bare-metal Kata node). Only the templates you list are published, so a project opts in to one or many. The image repo must be **public** (PoC — no registry credentials in the job). (Roadmap: a **private** repo with a Nomad docker `auth` block, or ECR with per-project toolchains.)

### Claude Code → LiteLLM AI gateway

The `dev-workspace` Claude Code CLI is pointed at the shared **LiteLLM AI gateway** (platform tier, `terraform/infra/llm-gateway.tf`), not the model provider directly. The gateway URL + model mapping are rendered per workspace at job launch into `/etc/claude-code/managed-settings.json` (the address is deployment-specific). Each project gets a scoped **LiteLLM virtual key** (allowed models + budget + rpm), minted automatically by `llm-gateway.tf` and stored in Vault KV (`secret/projects/<project>/llm`); it is rendered per session to the workspace `/secrets` tmpfs and read by Claude's `apiKeyHelper`, so it never lands on `/home/dev`. The **real provider key never reaches the workspace** — it lives only on the gateway (set `deepseek_api_key` in the **infra** tier). So only the gateway node needs **egress to `api.deepseek.com`**; the gateway also gives central audit (spend logs), per-project budgets, and a one-line swap to watsonx.ai. The workspace-facing model names (`deepseek-v4-pro`/`deepseek-v4-flash`) map to the real backend in the gateway's `config.yaml` `model_list`.

## Namespace model — verification & teardown

This tier provisions a **Vault Enterprise namespace per project** (`vault_namespace.project`) and scopes every project Vault object — SSH CA, database engine, GitHub broker, KV coordinates, the `jwt-nomad` WIF backend + role, and the Boundary credential store — inside it (the per-resource `namespace = vault_namespace.project.path` argument). Isolation is **Vault-enforced**, not dependent on path-prefix policy correctness: a token minted in `<project>` can read only `<project>` paths. Workspace and service Nomad jobs set `vault { namespace = "<project>" }` so their WIF login targets the namespace backend.

Verified end-to-end on a throwaway `nstest` project (full apply → developer flow → teardown), then `acme` was **rebuilt fresh under this model** (2026-06-15). The four-point developer check:

- SSH session authenticates with a cert signed by the namespaced `ssh/<project>` CA (Boundary-injected).
- `git clone` works via the namespaced GitHub token (`github/<project>/token/dev-workspace`).
- MCP + LLM coordinates resolve from the namespace KV via workload-identity reads.
- Claude Code LLM traffic flows through the gateway.

Per-engine probes (each resolves in-namespace; a root-scoped read of a project path does **not**):

```bash
export VAULT_SKIP_VERIFY=true
vault read   -namespace=<project> ssh/<project>/config/ca
vault read   -namespace=<project> database/<project>/creds/dev-workspace-ro
vault kv get -namespace=<project> -mount=secret projects/<project>/mcp
vault kv get -namespace=<project> -mount=secret projects/<project>/llm
vault read   -namespace=<project> github/<project>/token/dev-workspace
vault kv get -mount=secret projects/<project>/mcp   # root scope → "No value found" (isolation holds)
```

### Teardown — `terraform destroy` and the lease-revocation snag

`terraform destroy -var-file=project-<name>.tfvars` removes the whole project tier including the Vault namespace. Two stanzas (`database/<project>` and `github/<project>`) can **fail to delete** with a `400` error like:

```
failed to revoke ".../creds/..." : failed to find entry for connection with name: "demo-db"
failed to revoke ".../token/..."  : could not parse private key ...
```

This is a destroy-ordering issue: Terraform deletes the engine's **config** (the DB connection / GitHub App key) before Vault revokes the **outstanding dynamic leases** it minted, so revocation has nothing to call and the mount won't unmount. Outstanding leases come from a still-running workspace (purge it first — `nomad job stop -namespace <project> -purge <ws-job>`), but a freshly-minted probe lease can trigger it too.

Fix: **force-revoke the stuck leases** (`-force` drops them from Vault storage even when live revocation fails), then re-run destroy — the now-leaseless mounts unmount cleanly:

```bash
export VAULT_SKIP_VERIFY=true
vault lease revoke -namespace=<project> -force -prefix database/<project>/creds/dev-workspace-ro
vault lease revoke -namespace=<project> -force -prefix github/<project>/token/dev-workspace
terraform destroy -var-file=project-<name>.tfvars -auto-approve   # re-run; clears database/ + github/ mounts + the namespace
```

After destroy, drop the throwaway Terraform workspace: `terraform workspace select default && terraform workspace delete <project>`. (A future enhancement — `depends_on` ordering so engine config outlives its leases — would remove the manual step; tracked as a roadmap item.)

## Onboarding steps

**Vault must be unsealed** (this tier creates Vault mounts/roles/tokens).

```bash
cd terraform/project       # sibling of terraform/infra
terraform init
cp terraform.tfvars.example project-acme.tfvars   # project_name, developers_group_name, workspace_templates, github_app_*  (deepseek_api_key now lives in the infra tier)
terraform workspace new project-acme               # one workspace per project
terraform apply -var-file=project-acme.tfvars
```

This creates everything in [What it creates](#what-it-creates) above, all scoped to `project-acme`. Onboard another project by repeating with a **new workspace + tfvars** — its Vault path, Boundary scope, Nomad namespace and GitHub App are fully separate.

`*.tfvars` are gitignored — never commit secrets.
