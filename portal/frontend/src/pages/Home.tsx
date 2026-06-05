import { ClickableTile } from '@carbon/react'
import { useNavigate } from 'react-router-dom'

export default function Home() {
  const nav = useNavigate()
  return (
    <div className="page">
      <h2 style={{ marginBottom: '0.5rem' }}>Secured Dev Workspace</h2>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1.5rem' }}>
        Provision and connect to on-demand development workspaces. Browse the projects your groups grant, or
        jump straight to the workspaces you already have.
      </p>
      <div className="card-grid">
        <ClickableTile onClick={() => nav('/projects')}>
          <h4>Projects</h4>
          <p style={{ color: 'var(--cds-text-secondary)' }}>The projects your IBM Verify groups grant access to.</p>
        </ClickableTile>
        <ClickableTile onClick={() => nav('/workspaces')}>
          <h4>My Workspaces</h4>
          <p style={{ color: 'var(--cds-text-secondary)' }}>Every workspace you own, across all projects.</p>
        </ClickableTile>
      </div>
    </div>
  )
}
