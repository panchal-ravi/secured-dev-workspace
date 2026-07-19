import {
  Header,
  HeaderName,
  HeaderMenuButton,
  HeaderGlobalBar,
  HeaderGlobalAction,
  SideNav,
  SideNavItems,
  SideNavLink,
  SideNavDivider,
  Dropdown,
} from '@carbon/react'
import {
  Logout,
  Dashboard,
  Folders,
  Application,
  Catalog,
  DataStructured,
  MachineLearningModel,
  UserMultiple,
  Bot,
  VolumeFileStorage,
  Asleep,
  Light,
} from '@carbon/icons-react'
import type { MouseEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { adminProjects, agentProjects, workspaceProjects, isPlatformAdmin, logout, Me } from '../api/client'

export default function AppHeader({
  me,
  theme,
  onToggleTheme,
  navExpanded,
  onNavExpandedChange,
}: {
  me: Me
  theme: 'white' | 'g100'
  onToggleTheme: () => void
  navExpanded: boolean
  onNavExpandedChange: (expanded: boolean) => void
}) {
  const nav = useNavigate()
  const { pathname } = useLocation()

  const go = (e: MouseEvent, path: string) => {
    e.preventDefault()
    // Leave the nav panel open on navigation — it only closes via the menu (X)
    // button, matching the IBM Verify shell.
    nav(path)
  }

  const admin = adminProjects(me)
  // Developer/consumer nav (Projects, My Workspaces, AI agents) is hidden from a
  // platform-admin who holds no explicit project role. Platform-admins are
  // group-members of projects (which grants the implicit project-user baseline),
  // so gating on capabilities alone wouldn't hide these; we gate on an explicit
  // project_roles grant. Non-platform-admins are unaffected — their group-based
  // access stays as-is.
  const hasProjectRole = (me.project_roles || []).length > 0
  const showDeveloperNav = !isPlatformAdmin(me) || hasProjectRole
  const items = [{ label: 'Home', path: '/', icon: Dashboard, current: pathname === '/' }]
  if (showDeveloperNav) {
    items.push({ label: 'Projects', path: '/projects', icon: Folders, current: pathname === '/projects' })
  }
  // Platform Admin onboarding plane — shown only to platform admins.
  if (isPlatformAdmin(me)) {
    items.push(
      { label: 'Projects (admin)', path: '/admin/projects', icon: Folders, current: pathname.startsWith('/admin/projects') },
      { label: 'LLM models', path: '/admin/llm-models', icon: MachineLearningModel, current: pathname.startsWith('/admin/llm-models') },
      { label: 'Base templates', path: '/admin/base-templates', icon: Catalog, current: pathname.startsWith('/admin/base-templates') },
    )
  }
  // Developer project switcher — a per-project dropdown driving Workspaces + AI
  // agents sub-links, mirroring the admin switcher below. Populated from the
  // projects where the user holds a developer capability (workspaces or
  // ai-agents), so a member of several projects can jump between them instead of
  // being pinned to the first one. Each sub-link is shown only when the active
  // project actually grants that capability.
  const wsProjects = workspaceProjects(me)
  const agentProjectList = agentProjects(me)
  const devProjects = Array.from(new Set([...wsProjects, ...agentProjectList])).sort()
  const devRouteMatch = pathname.match(/^\/projects\/([^/]+)(?:\/agents)?$/)
  const activeDevProject = devRouteMatch ? decodeURIComponent(devRouteMatch[1]) : devProjects[0]

  // Project admins get a single project SWITCHER (dropdown) + a fixed set of
  // per-project sub-nav links, instead of one flat entry per (project × section).
  // The active project is derived from the current route so deep-links stay in
  // sync; it falls back to the first administered project. The dropdown navigates
  // to the selected project's members page.
  const projectSections = [
    { key: 'members', label: 'members', icon: UserMultiple },
    { key: 'mcp-servers', label: 'mcp servers', icon: Catalog },
    { key: 'agent-templates', label: 'agent templates', icon: Bot },
    { key: 'templates', label: 'workspace templates', icon: Catalog },
    { key: 'shared-volumes', label: 'shared volumes', icon: VolumeFileStorage },
    { key: 'github', label: 'github access', icon: DataStructured },
  ]
  const routeMatch = pathname.match(
    /^\/projects\/([^/]+)\/(members|mcp-servers|agent-templates|templates|shared-volumes|github)/,
  )
  const activeProject = routeMatch ? decodeURIComponent(routeMatch[1]) : admin[0]

  return (
    <Header aria-label="Secured Dev Workspace" className="cds--g100">
      <HeaderMenuButton
        aria-label={navExpanded ? 'Collapse navigation' : 'Expand navigation'}
        isCollapsible
        isActive={navExpanded}
        onClick={() => onNavExpandedChange(!navExpanded)}
      />
      <HeaderName href="/" prefix="Secured Dev" onClick={(e) => go(e, '/')}>
        Workspace
      </HeaderName>
      <HeaderGlobalBar>
        <span style={{ display: 'flex', alignItems: 'center', padding: '0 1rem', fontSize: '0.85rem' }}>
          {me.email}
        </span>
        <HeaderGlobalAction
          aria-label={theme === 'white' ? 'Switch to dark theme' : 'Switch to light theme'}
          onClick={onToggleTheme}
        >
          {theme === 'white' ? <Asleep /> : <Light />}
        </HeaderGlobalAction>
        <HeaderGlobalAction
          aria-label="Sign out"
          tooltipAlignment="end"
          onClick={async () => {
            // Navigate to Verify's RP-initiated logout so the shared SSO session
            // ends too (not just the portal cookie); fall back home if unavailable.
            const { logout_url } = await logout()
            window.location.assign(logout_url || '/')
          }}
        >
          <Logout />
        </HeaderGlobalAction>
      </HeaderGlobalBar>
      {/* Dark (g100) nav even in the white theme; isPersistent=false makes it a
          toggled panel — hidden when collapsed, 16rem when open — so the main
          content (offset in App) pushes right instead of being overlaid. */}
      <SideNav
        aria-label="Primary navigation"
        className="cds--g100"
        isPersistent={false}
        expanded={navExpanded}
        onOverlayClick={() => onNavExpandedChange(false)}
      >
        <SideNavItems>
          {items.map((it) => (
            <SideNavLink
              key={it.path}
              renderIcon={it.icon}
              href={it.path}
              isActive={it.current}
              onClick={(e: MouseEvent) => go(e, it.path)}
            >
              {it.label}
            </SideNavLink>
          ))}
          {showDeveloperNav && devProjects.length > 0 && activeDevProject && (
            <>
              <SideNavDivider />
              <div style={{ padding: '0.5rem 1rem' }}>
                <Dropdown
                  id="dev-project-switcher"
                  size="sm"
                  titleText="Project"
                  label="Select a project"
                  items={devProjects}
                  selectedItem={activeDevProject}
                  itemToString={(p) => p ?? ''}
                  onChange={({ selectedItem }) => {
                    if (selectedItem) nav(`/projects/${encodeURIComponent(selectedItem)}`)
                  }}
                />
              </div>
              {wsProjects.includes(activeDevProject) && (
                <SideNavLink
                  renderIcon={Application}
                  href={`/projects/${encodeURIComponent(activeDevProject)}`}
                  isActive={pathname === `/projects/${encodeURIComponent(activeDevProject)}`}
                  onClick={(e: MouseEvent) => go(e, `/projects/${encodeURIComponent(activeDevProject)}`)}
                >
                  workspaces
                </SideNavLink>
              )}
              {agentProjectList.includes(activeDevProject) && (
                <SideNavLink
                  renderIcon={Bot}
                  href={`/projects/${encodeURIComponent(activeDevProject)}/agents`}
                  isActive={/^\/projects\/[^/]+\/agents/.test(pathname)}
                  onClick={(e: MouseEvent) => go(e, `/projects/${encodeURIComponent(activeDevProject)}/agents`)}
                >
                  ai agents
                </SideNavLink>
              )}
            </>
          )}
          {admin.length > 0 && activeProject && (
            <>
              <SideNavDivider />
              <div style={{ padding: '0.5rem 1rem' }}>
                <Dropdown
                  id="project-switcher"
                  size="sm"
                  titleText="Administered project"
                  label="Select a project"
                  items={admin}
                  selectedItem={activeProject}
                  itemToString={(p) => p ?? ''}
                  onChange={({ selectedItem }) => {
                    if (selectedItem) nav(`/projects/${encodeURIComponent(selectedItem)}/members`)
                  }}
                />
              </div>
              {projectSections.map((s) => {
                const path = `/projects/${encodeURIComponent(activeProject)}/${s.key}`
                return (
                  <SideNavLink
                    key={s.key}
                    renderIcon={s.icon}
                    href={path}
                    isActive={pathname === path}
                    onClick={(e: MouseEvent) => go(e, path)}
                  >
                    {s.label}
                  </SideNavLink>
                )
              })}
            </>
          )}
        </SideNavItems>
      </SideNav>
    </Header>
  )
}
