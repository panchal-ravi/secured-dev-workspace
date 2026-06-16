import {
  Header,
  HeaderName,
  HeaderMenuButton,
  HeaderGlobalBar,
  HeaderGlobalAction,
  SideNav,
  SideNavItems,
  SideNavLink,
} from '@carbon/react'
import {
  Logout,
  Dashboard,
  Folders,
  Application,
  Catalog,
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
      { label: 'MCP servers', path: '/admin/mcp-servers', icon: Catalog, current: pathname.startsWith('/admin/mcp-servers') },
      { label: 'LLM models', path: '/admin/llm-models', icon: MachineLearningModel, current: pathname.startsWith('/admin/llm-models') },
    )
  }
  // Project admins get a Members entry per administered project.
  for (const p of adminProjects(me)) {
    items.push({
      label: `${p} · members`,
      path: `/projects/${encodeURIComponent(p)}/members`,
      icon: UserMultiple,
      current: pathname === `/projects/${encodeURIComponent(p)}/members`,
    })
  }
  // Project admins get an MCP-servers entry per administered project.
  for (const p of adminProjects(me)) {
    items.push({
      label: `${p} · mcp servers`,
      path: `/projects/${encodeURIComponent(p)}/mcp-servers`,
      icon: Catalog,
      current: pathname === `/projects/${encodeURIComponent(p)}/mcp-servers`,
    })
  }

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
        </SideNavItems>
      </SideNav>
    </Header>
  )
}
