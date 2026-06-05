# Project tier — one project per onboarding (`terraform/project/`)

The **project tier** of the three-tier model (`terraform/{infra,project,workspace}`).
A **flat root** (no child module) applied **once per project**, using a separate
**Terraform workspace** for each (the future portal will instead use a distinct backend
key per project). It reads the **platform** root's outputs via `terraform_remote_state`
(`../infra/terraform.tfstate`) — **no tokens or addresses go in tfvars**; just pick a
project name, the IBM Verify developers group for it, and the project's GitHub App.

See [`../README.md`](../README.md) for the three-tier overview and the end-to-end flow.

## What it creates

Applied **once per project** (one `terraform workspace` per project). Creates, scoped to the
project:

- A **Boundary project scope** under the org and a **Nomad namespace** (= project name).
- A path-prefixed **Vault SSH CA** (`ssh/<project>` mount + `dev-workspace` signing role with
  `permit-pty`/`permit-port-forwarding`, 5m/10m TTL, `key_id` = the developer's email) — per-project
  isolation by **Vault path**, *not* Vault Enterprise namespaces.
- The **Boundary Vault credential store** (authenticated with a dedicated least-privilege periodic token,
  not root) + the **SSH credential library** every workspace target in the project injects.
- A **per-project WIF role** on the shared `jwt-nomad` backend + a read-only policy exposing only this
  project's CA public key, and a namespace-scoped **Nomad ACL policy + binding rule** (defense-in-depth).
- A **GitHub App token broker** (`github/<project>` mount + config + a pre-scoped `dev-workspace`
  permission set, plus a WIF policy letting the workspace read a `contents:write` token from it) — the
  git push credential for every workspace in the project; no static PAT anywhere.
- The project's **Nomad job templates** ("flavors"), written as raw HCL to Vault KV at
  `secret/projects/<project>/job-templates/<name>`, **each with its OWN pinned image AND git
  repo** (from `workspace_templates`), plus an optional `node_pool` (`"gpu"` for the GPU
  flavor). At publish the project-static values (namespace, image, repo, WIF role, SSH CA path,
  GitHub/DB/DeepSeek paths) are baked in, leaving only the per-workspace placeholders — so both
  the developer tier and the Developer Portal render the **same** template and a developer never
  selects an image or repo.

The outputs (`project_scope_id`, `namespace`, `credential_library_id`, `ssh_ca_path`, `wif_role`,
`github_token_path`, `job_template_names`, `job_template_node_pools`, …) feed the developer tier;
a parallel `portal-descriptor` (with the same flavors, their picker metadata, and node pools) is
published to Vault KV for the Developer Portal.

## Prerequisite: a GitHub App (manual, one-time per project)

Like the IBM Verify setup on the platform tier, each project needs a GitHub App before the
first apply:

1. Create a **GitHub App** (org or personal) with repository permission **Contents:
   Read and write** (GitHub auto-adds **Metadata: Read-only**). No webhook needed.
2. **Install** it on the account/org that owns the project's repos, granting it those repos.
3. Collect three values for the tfvars: the **App ID**, the **Installation ID** (from the
   installation URL `…/installations/<id>`), and a generated **private key** `.pem`. If the
   key is PKCS#8 (`-----BEGIN PRIVATE KEY-----`), convert it to the PKCS#1 form the plugin
   expects: `openssl rsa -in key.pem -out key.pkcs1.pem`.

## Prerequisite: build the workspace images (one per flavor)

The project **owns the workspace toolchain images** — each job template ("flavor") under
`templates/` is pinned to an image the project team builds and pushes. Build a **public**,
multi-arch manifest (`linux/amd64` + `linux/arm64`) so it runs on the EC2 node (amd64)
regardless of build host; Nomad pulls it at launch (`force_pull = true`). One image per
template, from `images/<name>/`:

```bash
docker login                                            # authenticate to Docker Hub
docker buildx create --name multiarch --use --bootstrap # docker-container driver (once)
docker buildx build --platform linux/amd64,linux/arm64 \
  -t <dockerhub-user>/dev-workspace:poc --push images/dev-workspace
```

The **GPU flavor** image builds from `images/gpu-workspace/` (a CUDA-devel base with full
dev-workspace parity — `nvidia-smi`/`nvcc` plus Claude/DB-MCP/git). It only runs on the
amd64 GPU node, so a single-arch `linux/amd64` build is enough; it still pulls at launch.

Then add an entry per template to `workspace_templates` (see tfvars below) mapping the
template name to its pushed `image`, its `git_repo_url`, a picker `label`/`description`,
and an optional `node_pool` (`"gpu"` for the GPU flavor — it places the workspace on the GPU
node and requests an `nvidia/gpu` device). Only the templates you list are published, so a
project opts in to one or many. The image repo must be **public** (PoC — no registry
credentials in the job). (Roadmap: a **private** repo with a Nomad docker `auth` block, or ECR
with per-project toolchains.)

### Claude Code → DeepSeek

The `dev-workspace` image bakes the **Claude Code CLI pointed at DeepSeek** instead of
Anthropic (`/etc/claude-code/managed-settings.json`: DeepSeek's Anthropic-compatible base
URL + model names, per DeepSeek's Claude Code integration docs). The **DeepSeek API key**
is **not** baked — it is stored in Vault KV (`deepseek_api_key` tfvar →
`secret/projects/<project>/deepseek`), rendered per session to the workspace `/secrets`
tmpfs, and read by Claude's `apiKeyHelper`, so it never lands on `/home/dev`. The workspace
node must have **egress to `api.deepseek.com`** (HTTPS). Confirm the model ids in the
Dockerfile against your DeepSeek dashboard.

## Onboarding steps

**Vault must be unsealed** (this tier creates Vault mounts/roles/tokens).

```bash
cd terraform/project       # sibling of terraform/infra
terraform init
cp terraform.tfvars.example project-acme.tfvars   # project_name, developers_group_name, workspace_templates, deepseek_api_key, github_app_*
terraform workspace new project-acme               # one workspace per project
terraform apply -var-file=project-acme.tfvars
```

This creates everything in [What it creates](#what-it-creates) above, all scoped to
`project-acme`. Onboard another project by repeating with a **new workspace + tfvars** — its
Vault path, Boundary scope, Nomad namespace and GitHub App are fully separate.

`*.tfvars` are gitignored — never commit secrets.
