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

export interface McpServer {
  name: string
  image: string
  command?: string[]
  env?: Record<string, string>
  secret_refs?: Record<string, string>
  transport: string
  port: number
  path?: string
  namespace: string
  job_id?: string
  peer_id?: string
  gateway_url?: string
  status: string
  version: number
  test_result?: McpTestResult
  created_by?: string
  created_at: string
  updated_at: string
}

export interface DeployMcpInput {
  name: string
  image: string
  command?: string[]
  env?: Record<string, string>
  secret_refs?: Record<string, string>
  transport: string
  port: number
  path?: string
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

export function listMcpServers(): Promise<McpServer[]> {
  return fetch(`${adminBase}/mcp-servers`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.servers as McpServer[]) || [])
}

export function deployMcpServer(input: DeployMcpInput): Promise<McpServer> {
  return post(`${adminBase}/mcp-servers`, input)
}

export function testMcpServer(name: string): Promise<McpServer> {
  return post(`${adminBase}/mcp-servers/${encodeURIComponent(name)}/test`)
}

export function publishMcpServer(
  name: string,
  blueprintRef?: { id: string; version: number; content_hash: string },
): Promise<McpServer> {
  return post(
    `${adminBase}/mcp-servers/${encodeURIComponent(name)}/publish`,
    blueprintRef ? { blueprint_ref: blueprintRef } : undefined,
  )
}

export function deleteMcpServer(name: string): Promise<void> {
  return fetch(`${adminBase}/mcp-servers/${encodeURIComponent(name)}`, {
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

export function grantProjectRole(project: string, subject: string): Promise<ProjectRole> {
  return post(`/api/projects/${encodeURIComponent(project)}/roles`, { subject, role: 'project-admin' })
}

export function revokeProjectRole(project: string, subject: string): Promise<void> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/roles/${encodeURIComponent(subject)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// ---- Project Admin: MCP servers ----

export interface DeployableType {
  name: string
  image: string
  transport: string
  params: { name: string; type: string; required: boolean; prompt?: string }[]
  allow_extra_grants?: boolean
}

// PathGrant is a project-admin-supplied additional Vault path grant applied at deploy
// time. Confined to the project's own Vault namespace; sudo/root are rejected server-side.
export interface PathGrant {
  path: string
  capabilities: string[]
}

export interface ProjectMcpServer {
  project: string
  name: string
  status: string
  blueprint_ref: { id: string; version: number; content_hash: string }
  job_id?: string
  peer_id?: string
  gateway_url?: string
  test_result?: McpTestResult
  running?: boolean
  created_by?: string
  created_at: string
  updated_at: string
}

export interface ProjectMcpCatalog {
  deployable: DeployableType[]
  deployed: ProjectMcpServer[]
}

export function listProjectMcp(project: string): Promise<ProjectMcpCatalog> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/mcp-servers`, { credentials: 'include' }).then(asJSON)
}

export function deployProjectMcp(
  project: string,
  serverType: string,
  params: Record<string, string>,
  extraGrants?: PathGrant[],
): Promise<ProjectMcpServer> {
  const body: { server_type: string; params: Record<string, string>; extra_grants?: PathGrant[] } = {
    server_type: serverType,
    params,
  }
  if (extraGrants && extraGrants.length > 0) body.extra_grants = extraGrants
  return post(`/api/projects/${encodeURIComponent(project)}/mcp-servers`, body)
}

export function testProjectMcp(project: string, name: string): Promise<ProjectMcpServer> {
  return post(`/api/projects/${encodeURIComponent(project)}/mcp-servers/${encodeURIComponent(name)}/test`)
}

export function deleteProjectMcp(project: string, name: string): Promise<void> {
  return fetch(`/api/projects/${encodeURIComponent(project)}/mcp-servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
    credentials: 'include',
  }).then(expectOK)
}

// ---- Platform Admin: credential blueprints ----

export interface ParamSpec {
  name: string
  type: 'string' | 'int' | 'secret'
  required: boolean
  prompt?: string
}

export interface EngineSpec {
  type: string // "database" | "kv-v2"
  plugin?: string
  mount_path_tpl: string
}

export interface RoleSpec {
  name_tpl: string
  creation_statements: string[]
  default_ttl_seconds: number
  max_ttl_seconds: number
}

export interface WifRoleSpec {
  name_tpl: string
  token_ttl: string
}

export interface JobCredentialSpec {
  env_templates?: Record<string, string>
}

// BlueprintManifest mirrors backend internal/blueprint.BlueprintManifest. A
// manifest is immutable + content-hashed; a change is a new version.
export interface BlueprintManifest {
  id: string
  version: number
  class: 'A' | 'B' | 'C'
  description: string
  engines?: EngineSpec[]
  role?: RoleSpec
  policy_tpl: string
  wif_role: WifRoleSpec
  params?: ParamSpec[]
  job_credential?: JobCredentialSpec
  allow_extra_grants?: boolean
}

export interface BlueprintCheck {
  name: string
  passed: boolean
  detail?: string
}

export interface ValidationResult {
  passed: boolean
  checks: BlueprintCheck[]
  message?: string
  at: string
}

// Blueprint is the control-plane row (refs/metadata only — the manifest body
// lives in Vault KV, never returned by the list).
export interface Blueprint {
  id: string
  version: number
  class: string
  content_hash: string
  status: string // "draft" | "validated" | "published"
  validation?: ValidationResult
  created_by?: string
  created_at: string
  updated_at: string
}

export function listBlueprints(): Promise<Blueprint[]> {
  return fetch(`${adminBase}/blueprints`, { credentials: 'include' })
    .then(asJSON)
    .then((d) => (d.blueprints as Blueprint[]) || [])
}

export function createBlueprint(manifest: BlueprintManifest): Promise<Blueprint> {
  return post(`${adminBase}/blueprints`, manifest)
}

export function validateBlueprint(id: string, version: number): Promise<Blueprint> {
  return post(`${adminBase}/blueprints/${encodeURIComponent(id)}/${version}/validate`)
}

export function publishBlueprint(id: string, version: number): Promise<Blueprint> {
  return post(`${adminBase}/blueprints/${encodeURIComponent(id)}/${version}/publish`)
}
