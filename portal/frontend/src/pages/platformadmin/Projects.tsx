import { useEffect, useState } from 'react'
import {
  Button,
  InlineNotification,
  Loading,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { listProjects, Project } from '../../api/client'
import NewProjectModal from '../../components/NewProjectModal'

// Platform-admin Projects: list every project and create a new one (its Vault
// namespace, Nomad namespace, Boundary scope, descriptor, and first project-admin).
export default function Projects() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [modalOpen, setModalOpen] = useState(false)

  const refresh = () =>
    listProjects()
      .then(setProjects)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  if (loading) return <Loading withOverlay description="Loading projects" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h2>Projects</h2>
        <Button renderIcon={Add} onClick={() => setModalOpen(true)}>
          New project
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0.5rem 0 1rem' }}>
        Create and bootstrap a project (Vault namespace, Nomad namespace, Boundary scope, descriptor)
        and its first project-admin. Per-project engines are set up from the project admin view.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {projects.length === 0 ? (
        <p>No projects yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Namespace</TableHeader>
                <TableHeader>Flavors</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {projects.map((p) => (
                <TableRow key={p.name}>
                  <TableCell>{p.name}</TableCell>
                  <TableCell>{p.namespace}</TableCell>
                  <TableCell>
                    <Tag type="gray">{p.flavors.length}</Tag>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <NewProjectModal open={modalOpen} onClose={() => setModalOpen(false)} onCreated={refresh} />
    </div>
  )
}
