import { useEffect, useState } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { Loading, Theme } from '@carbon/react'
import { getMe, isPlatformAdmin, Me } from './api/client'
import { MeContext } from './me'
import AppHeader from './components/AppHeader'
import Login from './pages/Login'
import Home from './pages/Home'
import Projects from './pages/Projects'
import ProjectPage from './pages/Project'
import Workspaces from './pages/Workspaces'
import McpServers from './pages/platformadmin/McpServers'
import LlmModels from './pages/platformadmin/LlmModels'
import Blueprints from './pages/platformadmin/Blueprints'
import Members from './pages/projectadmin/Members'
import ProjectMcpServers from './pages/projectadmin/McpServers'

type ThemeName = 'white' | 'g100'

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [navExpanded, setNavExpanded] = useState(false)
  const [theme, setTheme] = useState<ThemeName>(
    () => (localStorage.getItem('portal-theme') as ThemeName) || 'white',
  )

  useEffect(() => {
    getMe()
      .then(setMe)
      .catch(() => setMe(null))
      .finally(() => setLoading(false))
  }, [])

  const toggleTheme = () => {
    setTheme((t) => {
      const next = t === 'white' ? 'g100' : 'white'
      localStorage.setItem('portal-theme', next)
      return next
    })
  }

  if (loading) return <Loading withOverlay description="Loading" />
  if (!me)
    return (
      <Theme theme={theme} className="app-shell">
        <Login />
      </Theme>
    )

  return (
    <MeContext.Provider value={me}>
      <Theme theme={theme} className="app-shell">
        <AppHeader
          me={me}
          theme={theme}
          onToggleTheme={toggleTheme}
          navExpanded={navExpanded}
          onNavExpandedChange={setNavExpanded}
        />
        {/* Offset the fixed Carbon header (3rem tall); push the content right by the
            side-nav width (16rem) when the nav panel is open, so it pushes rather
            than overlays. */}
        <main
          style={{
            marginTop: '3rem',
            marginLeft: navExpanded ? '16rem' : 0,
            transition: 'margin-left 0.11s cubic-bezier(0.2, 0, 1, 0.9)',
          }}
        >
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/projects" element={<Projects />} />
          <Route path="/projects/:name" element={<ProjectPage />} />
          <Route path="/workspaces" element={<Workspaces />} />
          {isPlatformAdmin(me) && <Route path="/admin/mcp-servers" element={<McpServers />} />}
          {isPlatformAdmin(me) && <Route path="/admin/llm-models" element={<LlmModels />} />}
          {isPlatformAdmin(me) && <Route path="/admin/blueprints" element={<Blueprints />} />}
          <Route path="/projects/:name/members" element={<Members />} />
          <Route path="/projects/:name/mcp-servers" element={<ProjectMcpServers />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </main>
      </Theme>
    </MeContext.Provider>
  )
}
