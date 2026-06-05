import { useCallback, useEffect, useState } from 'react'
import { InlineNotification, Loading } from '@carbon/react'
import { listAllWorkspaces, Workspace } from '../api/client'
import WorkspaceCard from '../components/WorkspaceCard'

export default function Workspaces() {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')

  const refresh = useCallback(() => {
    return listAllWorkspaces()
      .then(setWorkspaces)
      .catch((e) => setErr(e.message))
  }, [])

  useEffect(() => {
    setLoading(true)
    refresh().finally(() => setLoading(false))
  }, [refresh])

  // Mirror the project page: poll while anything is still starting up.
  useEffect(() => {
    if (!workspaces.some((w) => w.status === 'pending')) return
    const id = setInterval(refresh, 4000)
    return () => clearInterval(id)
  }, [workspaces, refresh])

  if (loading) return <Loading withOverlay description="Loading workspaces" />

  // Group by owning project so each card is attributed.
  const byProject: Record<string, Workspace[]> = {}
  for (const w of workspaces) {
    if (!byProject[w.project]) byProject[w.project] = []
    byProject[w.project].push(w)
  }

  return (
    <div className="page">
      <h2 style={{ marginBottom: '1rem' }}>My workspaces</h2>
      {err && <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onClose={() => setErr('')} />}
      {workspaces.length === 0 && !err && <p>You have no workspaces yet. Open a project to create one.</p>}
      {Object.entries(byProject).map(([project, wss]) => (
        <div key={project} style={{ marginBottom: '1.5rem' }}>
          <h4 style={{ marginBottom: '0.5rem', color: 'var(--cds-text-secondary)' }}>{project}</h4>
          <div className="card-grid">
            {wss.map((w) => (
              <WorkspaceCard key={w.name} ws={w} onChanged={refresh} />
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
