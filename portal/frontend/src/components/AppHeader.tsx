import { useState } from 'react'
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
import { Logout, Dashboard, Folders, Application, Asleep, Light } from '@carbon/icons-react'
import type { MouseEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { logout, Me } from '../api/client'

export default function AppHeader({
  me,
  theme,
  onToggleTheme,
}: {
  me: Me
  theme: 'white' | 'g100'
  onToggleTheme: () => void
}) {
  const nav = useNavigate()
  const { pathname } = useLocation()
  const [expanded, setExpanded] = useState(false)

  const go = (e: MouseEvent, path: string) => {
    e.preventDefault()
    setExpanded(false)
    nav(path)
  }

  const items = [
    { label: 'Home', path: '/', icon: Dashboard, current: pathname === '/' },
    { label: 'Projects', path: '/projects', icon: Folders, current: pathname.startsWith('/projects') },
    { label: 'My Workspaces', path: '/workspaces', icon: Application, current: pathname.startsWith('/workspaces') },
  ]

  return (
    <Header aria-label="Developer Portal">
      <HeaderMenuButton
        aria-label={expanded ? 'Collapse navigation' : 'Expand navigation'}
        isCollapsible
        isActive={expanded}
        onClick={() => setExpanded((v) => !v)}
      />
      <HeaderName href="/" prefix="Secured Dev" onClick={(e) => go(e, '/')}>
        Developer Portal
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
      <SideNav
        aria-label="Primary navigation"
        isRail
        expanded={expanded}
        onOverlayClick={() => setExpanded(false)}
        onSideNavBlur={() => setExpanded(false)}
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
