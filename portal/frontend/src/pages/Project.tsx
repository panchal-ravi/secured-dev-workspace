import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { Button, InlineNotification, Loading } from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { listWorkspaces, Project, Workspace } from '../api/client'
import WorkspaceCard from '../components/WorkspaceCard'
import CreateWorkspaceModal from '../components/CreateWorkspaceModal'
import BoundaryAuth from '../components/BoundaryAuth'

export default function ProjectPage() {
  const { name } = useParams()
  const [project, setProject] = useState<Project | null>(null)
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState(false)

  const refresh = useCallback(() => {
    if (!name) return Promise.resolve()
    return listWorkspaces(name)
      .then((d) => {
        setProject(d.project)
        setWorkspaces(d.workspaces || [])
      })
      .catch((e) => setErr(e.message))
  }, [name])

  useEffect(() => {
    setLoading(true)
    refresh().finally(() => setLoading(false))
  }, [refresh])

  // A freshly-registered Nomad job reports "pending" until its allocation is
  // running, so poll until nothing is pending — the card flips to "running"
  // without a manual reload.
  useEffect(() => {
    if (!workspaces.some((w) => w.status === 'pending')) return
    const id = setInterval(refresh, 4000)
    return () => clearInterval(id)
  }, [workspaces, refresh])

  if (loading && !project) return <Loading withOverlay description="Loading workspaces" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>{name}</h2>
        <Button renderIcon={Add} onClick={() => setOpen(true)}>
          New workspace
        </Button>
      </div>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onClose={() => setErr('')} />
      )}
      {workspaces.length === 0 && !err && <p>No workspaces yet. Create one to get started.</p>}
      {name && workspaces.length > 0 && (
        <BoundaryAuth
          project={name}
          command={workspaces[0].boundary_authenticate_cmd}
          addr={workspaces[0].boundary_addr}
          authMethodId={workspaces[0].boundary_auth_method_id}
        />
      )}
      <div className="card-grid">
        {workspaces.map((w) => (
          <WorkspaceCard key={w.name} ws={w} onChanged={refresh} />
        ))}
      </div>
      {project && (
        <CreateWorkspaceModal
          open={open}
          project={project}
          onClose={() => setOpen(false)}
          onCreated={() => {
            setOpen(false)
            refresh()
          }}
        />
      )}
    </div>
  )
}
