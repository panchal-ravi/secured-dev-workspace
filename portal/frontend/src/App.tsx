import { useEffect, useState } from 'react'
import { Routes, Route, Navigate } from 'react-router-dom'
import { Loading, Theme } from '@carbon/react'
import { getMe, Me } from './api/client'
import { MeContext } from './me'
import AppHeader from './components/AppHeader'
import Login from './pages/Login'
import Home from './pages/Home'
import Projects from './pages/Projects'
import ProjectPage from './pages/Project'
import Workspaces from './pages/Workspaces'

type ThemeName = 'white' | 'g100'

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
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
  if (!me) return <Login />

  return (
    <MeContext.Provider value={me}>
      <Theme theme={theme} className="app-shell">
        <AppHeader me={me} theme={theme} onToggleTheme={toggleTheme} />
        {/* Offset the fixed Carbon header (3rem tall) and the collapsed SideNav rail (3rem wide). */}
        <main style={{ marginTop: '3rem', marginLeft: '3rem' }}>
        <Routes>
          <Route path="/" element={<Home />} />
          <Route path="/projects" element={<Projects />} />
          <Route path="/projects/:name" element={<ProjectPage />} />
          <Route path="/workspaces" element={<Workspaces />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </main>
      </Theme>
    </MeContext.Provider>
  )
}
