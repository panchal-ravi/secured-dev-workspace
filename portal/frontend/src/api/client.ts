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
  status: string
  port: number
  target_id: string
  alias: string
  features: Feature[]
  proxycommand_config: string
  transparent_config: string
  boundary_authenticate_cmd: string
}

export interface Me {
  email: string
  handle: string
  groups: string[]
  local_ssh: boolean
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

export function logout(): Promise<Response> {
  return fetch('/auth/logout', { method: 'POST', credentials: 'include' })
}
