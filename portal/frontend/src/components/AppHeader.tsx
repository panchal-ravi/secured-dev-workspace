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
  Asleep,
  Light,
} from '@carbon/icons-react'
import type { MouseEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { adminProjects, isPlatformAdmin, logout, Me } from '../api/client'

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

  const items = [
    { label: 'Home', path: '/', icon: Dashboard, current: pathname === '/' },
    { label: 'Projects', path: '/projects', icon: Folders, current: pathname.startsWith('/projects') },
    { label: 'My Workspaces', path: '/workspaces', icon: Application, current: pathname.startsWith('/workspaces') },
  ]
  // Platform Admin onboarding plane — shown only to platform admins.
  if (isPlatformAdmin(me)) {
    items.push(
      { label: 'Projects (admin)', path: '/admin/projects', icon: Folders, current: pathname.startsWith('/admin/projects') },
      { label: 'MCP servers', path: '/admin/mcp-servers', icon: Catalog, current: pathname.startsWith('/admin/mcp-servers') },
      { label: 'LLM models', path: '/admin/llm-models', icon: MachineLearningModel, current: pathname.startsWith('/admin/llm-models') },
      { label: 'Vault blueprints', path: '/admin/blueprints', icon: DataStructured, current: pathname.startsWith('/admin/blueprints') },
      { label: 'Base templates', path: '/admin/base-templates', icon: Catalog, current: pathname.startsWith('/admin/base-templates') },
    )
  }
  // Project admins get a single project SWITCHER (dropdown) + a fixed set of
  // per-project sub-nav links, instead of one flat entry per (project × section).
  // The active project is derived from the current route so deep-links stay in
  // sync; it falls back to the first administered project. The dropdown navigates
  // to the selected project's members page.
  const admin = adminProjects(me)
  const projectSections = [
    { key: 'members', label: 'members', icon: UserMultiple },
    { key: 'mcp-servers', label: 'mcp servers', icon: Catalog },
    { key: 'templates', label: 'templates', icon: Catalog },
    { key: 'engines', label: 'engines', icon: DataStructured },
  ]
  const routeMatch = pathname.match(/^\/projects\/([^/]+)\/(members|mcp-servers|templates|engines)/)
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
            await logout()
            window.location.assign('/')
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
