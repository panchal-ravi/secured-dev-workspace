import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ClickableTile, InlineNotification, Loading, Tag } from '@carbon/react'
import { listProjects, Project } from '../api/client'

export default function Projects() {
  const [projects, setProjects] = useState<Project[]>([])
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(true)
  const nav = useNavigate()

  useEffect(() => {
    listProjects()
      .then(setProjects)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <Loading withOverlay description="Loading projects" />

  return (
    <div className="page">
      <h2 style={{ marginBottom: '1rem' }}>Your projects</h2>
      {err && <InlineNotification kind="error" title="Error" subtitle={err} lowContrast />}
      {projects.length === 0 && !err && <p>No projects are assigned to your groups yet.</p>}
      <div className="card-grid">
        {projects.map((p) => (
          <ClickableTile key={p.name} onClick={() => nav(`/projects/${p.name}`)}>
            <h4>{p.name}</h4>
            <p style={{ color: 'var(--cds-text-secondary)' }}>namespace: {p.namespace}</p>
            <div style={{ marginTop: '0.5rem' }}>
              <Tag type="blue">
                {p.flavors.length} flavor{p.flavors.length === 1 ? '' : 's'}
              </Tag>
            </div>
          </ClickableTile>
        ))}
      </div>
    </div>
  )
}
