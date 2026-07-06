// Thin fetch wrappers for the portal API. Cookies carry the session, so every
// request is same-origin with credentials included.

export interface Feature {
  key: string
  label: string
  description: string
}

export interface Flavor {
  name: string
  label?: string
  description?: string
  git_repo_url?: string
  image?: string
  node_pool?: string
  features: Feature[]
}

export interface Project {
  name: string
  namespace: string
  flavors: Flavor[]
}

export interface Workspace {
  name: string
  workspace_name: string
  project: string
  flavor: string
  status: string
  port: number
  target_id: string
  alias: string
  features: Feature[]
  proxycommand_config: string
  transparent_config: string
  boundary_authenticate_cmd: string
  boundary_addr: string
  boundary_auth_method_id: string
  user: string
}

// TerminalOption is a terminal emulator the portal can launch. The list is
// derived in the browser from the user's OS (see BoundaryAuth), and the chosen
// id is sent to the backend's boundary-authenticate endpoint.
export interface TerminalOption {
  id: string
  label: string
}

export interface Me {
  email: string
  handle: string
  groups: string[]
  roles: string[]
  project_roles?: { project: string; role: string }[]
  local_ssh: boolean
}

export function isPlatformAdmin(me: Me | null): boolean {
  return !!me && (me.roles || []).includes('platform-admin')
}

// adminProjects returns the projects where the user holds project-admin, used to
// gate the Members nav/page. Falls back to [] when the field is absent.
export function adminProjects(me: Me | null): string[] {
  return (me?.project_roles || [])
    .filter((r) => r.role === 'project-admin')
    .map((r) => r.project)
}

async function asJSON(r: Response) {
  if (!r.ok) {
    const body = await r.json().catch(() => ({}))
    throw new Error(body.error || r.statusText)
  }
  return r.json()
}

async function expectOK(r: Response) {
  if (!r.ok) {
    const body = await r.json().catch(() => ({}))
    throw new Error(body.error || r.statusText)
  }
}

export function getMe(): Promise<Me> {
  return fetch('/api/me', { credentials: 'include' }).then((r) => {
    if (!r.ok) throw new Error('unauthenticated')
    return r.json()
  })
}

export function listProjects(): Promise<Project[]> {
  return fetch('/api/projects', { credentials: 'include' })
    .then(asJSON)
    .then((d) => d.projects as Project[])
}

export function listWorkspaces(name: string): Promise<{ project: Project; workspaces: Workspace[] }> {
  return fetch(`/api/projects/${encodeURIComponent(name)}/workspaces`, { credentials: 'include' }).then(asJSON)
}

export function listAllWorkspaces(): Promise<Workspace[]> {
  return fetch('/api/workspaces', { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.workspaces as Workspace[]) || [])
}

export function createWorkspace(name: string, body: { flavor?: string }): Promise<Workspace> {
  return fetch(`/api/projects/${encodeURIComponent(name)}/workspaces`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }).then(asJSON)
}

function lifecycle(project: string, ws: string, action: 'stop' | 'start' | 'destroy'): Promise<void> {
  const base = `/api/projects/${encodeURIComponent(project)}/workspaces/${encodeURIComponent(ws)}`
  const init: RequestInit =
    action === 'destroy'
      ? { method: 'DELETE', credentials: 'include' }
      : { method: 'POST', credentials: 'include' }
  const url = action === 'destroy' ? base : `${base}/${action}`
  return fetch(url, init).then(expectOK)
}

export function stopWorkspace(project: string, ws: string): Promise<void> {
  return lifecycle(project, ws, 'stop')
}

export function startWorkspace(project: string, ws: string): Promise<void> {
  return lifecycle(project, ws, 'start')
}

export function destroyWorkspace(project: string, ws: string): Promise<void> {
  return lifecycle(project, ws, 'destroy')
}

export function fetchLogs(project: string, ws: string, type: 'stdout' | 'stderr'): Promise<string> {
  const base = `/api/projects/${encodeURIComponent(project)}/workspaces/${encodeURIComponent(ws)}`
  return fetch(`${base}/logs?type=${type}`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.logs as string) || '')
}

// writeSSHConfig asks the (local-mode) backend to upsert this workspace's SSH
// config block and returns the Host label to hand to an IDE deep link.
export function writeSSHConfig(project: string, ws: string): Promise<string> {
  const base = `/api/projects/${encodeURIComponent(project)}/workspaces/${encodeURIComponent(ws)}`
  return fetch(`${base}/ssh-config`, { method: 'POST', credentials: 'include' })
    .then(asJSON)
    .then((d) => d.host as string)
}

// The portal backend is remote, so it can't touch the developer's machine. These
// secured-ws:// deep links are handed to the locally-installed helper, which runs
// the one-time Boundary login (authenticate) or writes the SSH config block and
// opens the IDE (connect). The helper validates every parameter; we only assemble
// and URL-encode them here.
const SCHEME = 'secured-ws'

// HELPER_DOWNLOAD is the portal-served macOS helper bundle (built by
// portal/helper/macos/build.sh into backend/web/helper).
export const HELPER_DOWNLOAD = '/helper/SecuredWS-macos.zip'

export function boundaryAuthLink(addr: string, authMethodId: string, terminal: string): string {
  const q = new URLSearchParams({ addr, auth_method_id: authMethodId, terminal })
  return `${SCHEME}://authenticate?${q.toString()}`
}

export function connectLink(p: {
  host: string
  user: string
  targetId: string
  addr: string
  ide: string
}): string {
  const q = new URLSearchParams({
    host: p.host,
    user: p.user,
    target_id: p.targetId,
    addr: p.addr,
    ide: p.ide,
  })
  return `${SCHEME}://connect?${q.toString()}`
}

// disconnectLink tells the helper to remove this workspace's managed Host block
// from ~/.ssh/config — fired when a workspace is destroyed so stale entries don't
// linger. The helper no-ops if the block was never written.
export function disconnectLink(host: string): string {
  const q = new URLSearchParams({ host })
  return `${SCHEME}://disconnect?${q.toString()}`
}

export function logout(): Promise<Response> {
  return fetch('/auth/logout', { method: 'POST', credentials: 'include' })
}

// ---- Platform Admin onboarding plane ----

export interface McpTestResult {
  passed: boolean
  tools_discovered: number
  own_server_ok: boolean
  admin_denied: boolean
  other_server_denied: boolean
  other_server_checked: boolean
  message?: string
  at: string
}

export interface LlmTestResult {
  passed: boolean
  completion_ok: boolean
  rate_limit_enforced: boolean
  revoke_enforced: boolean
  message?: string
  at: string
}

// LlmModel is an inventory row. The LiteLLM gateway is the source of truth for
// which models exist (name/source); the Portal overlay adds the onboarding
// lifecycle (managed/status/test_result). Unmanaged models are served by the
// gateway with no Portal overlay (config-list or added out-of-band); orphaned
// rows are overlay entries with no live gateway model.
export interface LlmModel {
  name: string
  source?: string // "config" | "db"
  litellm_id?: string
  managed?: boolean
  orphaned?: boolean
  provider?: string
  backend_model?: string
  status?: string
  test_result?: LlmTestResult
  created_by?: string
}

export interface OnboardLlmInput {
  name: string
  provider: string
  backend_model: string
}

export interface SetProviderKeyInput {
  provider: string
  api_key: string
}

const adminBase = '/api/admin'

function post<T>(url: string, body?: unknown): Promise<T> {
  return fetch(url, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  }).then(asJSON)
}

// ---- Platform Admin: project create ----

export interface CreateProjectInput {
  project_name: string
  developers_group_name: string
  workspace_user?: string
  first_admin: string
}

// createProject bootstraps a project's Vault namespace + Nomad namespace +
// Boundary scope + descriptor and grants the first project-admin. Returns the
// written descriptor (project_name/namespace/...).
export function createProject(input: CreateProjectInput): Promise<{ project_name: string }> {
  return post(`${adminBase}/projects`, input)
}

// ProjectDetail is the platform-admin's full view of a project: the flattened
// descriptor plus the store row's lifecycle metadata.
export interface ProjectDetail {
  project_name: string
  namespace: string
  project_scope_id?: string
  credential_library_id?: string
  developers_group_name: string
  boundary_oidc_auth_method_id?: string
  instance_private_ip?: string
  workspace_user?: string
  github_configured?: boolean
  flavors?: Flavor[]
  status: string
  created_by?: string
  created_at: string
  updated_at: string
}

export function getAdminProject(name: string): Promise<ProjectDetail> {
  return fetch(`${adminBase}/projects/${encodeURIComponent(name)}`, { credentials: 'include' }).then(asJSON)
}

// updateAdminProject edits the project's developers group (the only safe edit —
// name/namespace key platform resources and the workspace user is baked into the
// provisioned engines). Applies to future requests; running workspaces untouched.
export function updateAdminProject(name: string, developers_group_name: string): Promise<ProjectDetail> {
  return fetch(`${adminBase}/projects/${encodeURIComponent(name)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ developers_group_name }),
  }).then(asJSON)
}

// deleteAdminProject tears the project down across every plane (Vault namespace,
// Nomad namespace/jobs, Boundary scope, gateway artifacts, LLM key, store rows).
// Partial failures keep the project visible with status=error; retry converges.
export function deleteAdminProject(name: string): Promise<void> {
  return fetch(`${adminBase}/projects/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

export function listLlmModels(): Promise<LlmModel[]> {
  return fetch(`${adminBase}/llm/models`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.models as LlmModel[]) || [])
}

export function onboardLlmModel(input: OnboardLlmInput): Promise<LlmModel> {
  return post(`${adminBase}/llm/models`, input)
}

// setProviderKey stores a provider API key in Vault (write-only — the server
// returns 204 and never echoes the key back).
export function setProviderKey(input: SetProviderKeyInput): Promise<void> {
  return fetch(`${adminBase}/llm/providers`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }).then(expectOK)
}

export function testLlmModel(name: string): Promise<LlmModel> {
  return post(`${adminBase}/llm/models/${encodeURIComponent(name)}/test`)
}

export function publishLlmModel(name: string): Promise<LlmModel> {
  return post(`${adminBase}/llm/models/${encodeURIComponent(name)}/publish`)
}

export function deleteLlmModel(name: string): Promise<void> {
  return fetch(`${adminBase}/llm/models/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// ---- Project Admin: member roles ----

export interface ProjectRole {
  project: string
  subject: string
  role: string
  granted_by: string
  granted_at: string
}

export function listProjectRoles(project: string): Promise<ProjectRole[]> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/roles`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.roles as ProjectRole[]) || [])
}

export function grantProjectRole(
  project: string,
  subject: string,
  role = 'project-admin',
): Promise<ProjectRole> {
  return post(`/api/projects/${encodeURIComponent(project)}/roles`, { subject, role })
}

export function revokeProjectRole(project: string, subject: string, role = 'project-admin'): Promise<void> {
  const q = new URLSearchParams({ role }).toString()
  return fetch(`/api/projects/${encodeURIComponent(project)}/roles/${encodeURIComponent(subject)}?${q}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// ---- Project Admin: MCP servers ----

// PathGrant is a project-admin-supplied additional Vault path grant applied at deploy
// time. Confined to the project's own Vault namespace; sudo/root are rejected server-side.
export interface PathGrant {
  path: string
  capabilities: string[]
}

export interface ParamSpec {
  name: string
  type: 'string' | 'int' | 'secret'
  required: boolean
  prompt?: string
}

// The credential types below mirror backend internal/blueprint.CredentialSpec.

export interface LogicalWrite {
  path: string
  data?: Record<string, unknown>
}

export interface DynamicSpec {
  engine: string // "database" | "aws" | ...
  mount: string // e.g. "database/postgres-mcp"
  configs?: LogicalWrite[]
  rotate_root_path?: string
  role?: LogicalWrite
  creds_path: string // relative to mount
  creds_caps?: string[] // default ["read"]
  lease_based?: boolean // default true
}

export interface StaticSpec {
  data: Record<string, string> // values may carry ${param}
}

export interface CredentialSpec {
  source: 'none' | 'wif-token' | 'static' | 'dynamic'
  token_ttl?: string
  params?: ParamSpec[]
  static?: StaticSpec
  dynamic?: DynamicSpec
  env_templates?: Record<string, string> // ${cred_path}/${param} render tokens + literal {{ }} consul-template
}

// McpInstanceInfo is the non-secret slice of the persisted blueprint instance
// record — what the deploy actually provisioned in the project's Vault namespace.
export interface McpInstanceInfo {
  namespace?: string
  wif_role_name?: string
  cred_path?: string
  extra_grants?: PathGrant[]
  mounts?: string[]
  policy_names?: string[]
}

export interface ProjectMcpServer {
  project: string
  name: string
  status: string
  image?: string
  command?: string[]
  env?: Record<string, string>
  transport?: string
  port?: number
  path?: string
  credential?: CredentialSpec
  params?: Record<string, string>
  blueprint_ref?: { id: string; version: number; content_hash: string } // legacy
  instance?: McpInstanceInfo
  job_id?: string
  peer_id?: string
  gateway_url?: string
  test_result?: McpTestResult
  created_by?: string
  created_at: string
  updated_at: string
}

export type ProjectMcpDeployed = ProjectMcpServer & { running: boolean }

// DeployMcpServerInput mirrors the backend's DeployInput: full server definition
// + credential config authored by the project-admin in one shot.
export interface DeployMcpServerInput {
  name: string
  image: string
  command?: string[]
  env?: Record<string, string>
  transport: string // "sse" | "streamable-http"
  port: number // container port; host side is dynamic
  path?: string // defaults /sse or /mcp by transport
  credential: CredentialSpec
  params?: Record<string, string>
  extra_grants?: PathGrant[]
}

export function listProjectMcp(project: string): Promise<{ deployed: ProjectMcpDeployed[] }> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/mcp-servers`, { credentials: 'include' }).then(asJSON)
}

export function deployProjectMcp(project: string, input: DeployMcpServerInput): Promise<ProjectMcpServer> {
  return post(`/api/projects/${encodeURIComponent(project)}/mcp-servers`, input)
}

export function testProjectMcp(project: string, name: string): Promise<ProjectMcpServer> {
  return post(`/api/projects/${encodeURIComponent(project)}/mcp-servers/${encodeURIComponent(name)}/test`)
}

// Edits a deployed server in place: the full container definition (sent whole,
// replacing what's stored) + the additional Vault path grants (the derived
// credential policy is re-applied server-side). Grant changes take effect
// immediately; a definition change resubmits the Nomad job (new address) and
// heals the gateway peer in place — workspaces keep working. Only if the heal
// fails is the wiring rebuilt (workspaces must then be recreated). The
// credential source cannot change: delete + redeploy.
export function updateProjectMcpServer(
  project: string,
  name: string,
  input: {
    image: string
    command?: string[]
    transport: string
    port: number
    path?: string
    env: Record<string, string>
    extra_grants: PathGrant[]
  },
): Promise<ProjectMcpServer> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/mcp-servers/${encodeURIComponent(name)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }).then(asJSON)
}

export function deleteProjectMcp(project: string, name: string): Promise<void> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/mcp-servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// ---- Platform Admin: base job templates ----

// TemplateFeature is one capability card surfaced on a workspace flavor.
export interface TemplateFeature {
  key: string
  label: string
  description: string
}

// BaseJobTemplate is a platform-authored generic Nomad workspace job template.
// Mutable with a version that bumps on publish; project templates snapshot the
// published source at create time.
export interface BaseJobTemplate {
  name: string
  label?: string
  description?: string
  status: string // "draft" | "published"
  version: number
  content_hash?: string
  draft_source: string
  published_source: string
  image?: string // container image baked into project templates (portal-admin owned)
  features?: TemplateFeature[]
  default_node_pool?: string
  runtime?: string
  created_by?: string
  created_at: string
  updated_at: string
}

export function listBaseTemplates(): Promise<BaseJobTemplate[]> {
  return fetch(`${adminBase}/base-templates`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.templates as BaseJobTemplate[]) || [])
}

export function getBaseTemplate(name: string): Promise<BaseJobTemplate> {
  return fetch(`${adminBase}/base-templates/${encodeURIComponent(name)}`, { credentials: 'include' }).then(asJSON)
}

export function updateBaseTemplateDraft(name: string, source: string, image: string): Promise<BaseJobTemplate> {
  return fetch(`${adminBase}/base-templates/${encodeURIComponent(name)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source, image }),
  }).then(asJSON)
}

export function publishBaseTemplate(name: string): Promise<BaseJobTemplate> {
  return post(`${adminBase}/base-templates/${encodeURIComponent(name)}/publish`)
}

// ---- Project Admin: project templates (create a flavor from a base template) ----

// BaseOption is a published base template a project-admin can build a flavor from.
export interface BaseOption {
  name: string
  label?: string
  description?: string
  version: number
  image?: string // baked into the project template; readonly to the project-admin
  default_node_pool?: string
  runtime?: string
  features?: TemplateFeature[]
}

// ProjectTemplate is a per-project flavor: a base template with the project-static
// placeholders baked in (pass-1); the per-workspace ${...} tokens stay for launch.
export interface AddonSecretFile {
  kv_field: string
  dest_file: string
  env?: string
}

export interface AddonEngine {
  mount: string
  type: string
  kv_path?: string
  secret_files?: AddonSecretFile[]
}

// TemplateAddons is a flavor's structured extension: MCP servers (from the catalog)
// and extra secret engines injected into the workspace template.
export interface TemplateAddons {
  mcp_servers?: string[]
  engines?: AddonEngine[]
}

export interface ProjectTemplate {
  project: string
  flavor: string
  base?: string
  base_version: number
  status: string
  rendered_source: string
  baked_base?: string
  label?: string
  description?: string
  image?: string
  git_repo_url?: string
  node_pool?: string
  features?: TemplateFeature[]
  addons?: TemplateAddons
  created_by?: string
  created_at: string
  updated_at: string
}

// Image is NOT accepted here — it is a property of the base template (portal-admin
// owned), baked in at create.
export interface CreateProjectTemplateInput {
  base: string
  flavor?: string
  git_repo_url: string
  label?: string
  description?: string
  node_pool?: string
}

export function listProjectBaseTemplates(project: string): Promise<BaseOption[]> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/base-templates`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d as BaseOption[]) || [])
}

export function listProjectTemplates(project: string): Promise<ProjectTemplate[]> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/templates`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d as ProjectTemplate[]) || [])
}

export function createProjectTemplate(project: string, in_: CreateProjectTemplateInput): Promise<ProjectTemplate> {
  return post(`/api/projects/${encodeURIComponent(project)}/templates`, in_)
}

// UpdateProjectTemplateInput edits an existing flavor. Empty fields keep their
// current value; a repo change re-bakes from the current published base. Editing
// never affects running workspaces (templates are read at launch time only).
export interface UpdateProjectTemplateInput {
  git_repo_url?: string
  label?: string
  description?: string
  node_pool?: string
}

export function updateProjectTemplate(
  project: string,
  flavor: string,
  in_: UpdateProjectTemplateInput,
): Promise<ProjectTemplate> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/templates/${encodeURIComponent(flavor)}`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(in_),
  }).then(asJSON)
}

export function deleteProjectTemplate(project: string, flavor: string): Promise<void> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/templates/${encodeURIComponent(flavor)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// updateTemplateAddons sets a flavor's structured add-ons (MCP servers + extra
// engines); the server provisions the engines/MCP wiring and re-renders the template.
export function updateTemplateAddons(project: string, flavor: string, addons: TemplateAddons): Promise<ProjectTemplate> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/templates/${encodeURIComponent(flavor)}/addons`, {
    method: 'PUT',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(addons),
  }).then(asJSON)
}

// ---- Project Admin: engine provisioning (SSH CA / GitHub App / LLM / Boundary) ----

export interface ProvisionEnginesInput {
  github_app_id: number
  github_app_installation_id: number
  github_app_private_key: string
  github_repositories?: string[]
}

// ProvisionResult is the (subset of the) project descriptor returned after a
// successful engine provision — the credential library id proves Boundary is wired.
export interface ProvisionResult {
  project_name: string
  namespace: string
  credential_library_id?: string
}

export function provisionProjectEngines(project: string, in_: ProvisionEnginesInput): Promise<ProvisionResult> {
  return post(`/api/projects/${encodeURIComponent(project)}/provision`, in_)
}

// EngineStatus is the non-secret provisioning state the Engines page reads. The
// GitHub App coordinates are echoed back for form prefill; never the private key.
export interface EngineStatus {
  provisioned: boolean
  github_configured: boolean
  github_app_id?: number
  github_app_installation_id?: number
  github_repositories?: string[]
  credential_library_id?: string
  status: string
}

export function getEngineStatus(project: string): Promise<EngineStatus> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/engines`, { credentials: 'include' }).then(asJSON)
}

// setGithubCredentials sets/updates the project's GitHub App config on the pre-existing
// github mount (the private key is write-only). The engines themselves are provisioned
// automatically at project-create.
export function setGithubCredentials(project: string, in_: ProvisionEnginesInput): Promise<{ status: string }> {
  return post(`/api/projects/${encodeURIComponent(project)}/engines/github`, in_)
}
