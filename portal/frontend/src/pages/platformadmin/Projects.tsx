import { useEffect, useState } from 'react'
import {
  Button,
  InlineNotification,
  Loading,
  Modal,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { listProjects, getAdminProject, updateAdminProject, deleteAdminProject, Project, ProjectDetail } from '../../api/client'
import NewProjectModal from '../../components/NewProjectModal'

// Platform-admin Projects: list every project, create a new one (its Vault
// namespace, Nomad namespace, Boundary scope, descriptor, and first project-admin),
// inspect a project's full descriptor, and edit its developers group. The project
// name/namespace and workspace user are immutable (they key platform resources).
export default function Projects() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [detail, setDetail] = useState<ProjectDetail | null>(null)
  const [detailErr, setDetailErr] = useState('')
  const [editGroup, setEditGroup] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleteFor, setDeleteFor] = useState<ProjectDetail | null>(null)
  const [confirmName, setConfirmName] = useState('')
  const [deleteErr, setDeleteErr] = useState('')

  const refresh = () =>
    listProjects()
      .then(setProjects)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  const openDetail = (name: string) => {
    setDetailErr('')
    getAdminProject(name)
      .then((d) => {
        setDetail(d)
        setEditGroup(d.developers_group_name)
      })
      .catch((e) => setErr((e as Error).message))
  }

  const doDelete = async () => {
    if (!deleteFor) return
    setBusy(true)
    setDeleteErr('')
    try {
      await deleteAdminProject(deleteFor.project_name)
      setDeleteFor(null)
      setDetail(null)
      await refresh()
    } catch (e) {
      // A partial teardown returns an error but is retryable to convergence.
      setDeleteErr((e as Error).message)
      await refresh()
    } finally {
      setBusy(false)
    }
  }

  const saveGroup = async () => {
    if (!detail) return
    setBusy(true)
    setDetailErr('')
    try {
      const d = await updateAdminProject(detail.project_name, editGroup.trim())
      setDetail(d)
      await refresh()
    } catch (e) {
      setDetailErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

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
                <TableHeader>Actions</TableHeader>
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
                  <TableCell>
                    <Button size="sm" kind="ghost" onClick={() => openDetail(p.name)}>
                      Details
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <NewProjectModal open={modalOpen} onClose={() => setModalOpen(false)} onCreated={refresh} />

      <Modal
        open={detail !== null}
        modalHeading={`Project — ${detail?.project_name || ''}`}
        primaryButtonText={busy ? 'Saving…' : 'Save group'}
        secondaryButtonText="Close"
        primaryButtonDisabled={busy || !editGroup.trim() || editGroup.trim() === detail?.developers_group_name}
        onRequestClose={() => setDetail(null)}
        onRequestSubmit={saveGroup}
      >
        {detail && (
          <>
            {detailErr && (
              <InlineNotification kind="error" title="Error" subtitle={detailErr} lowContrast onCloseButtonClick={() => setDetailErr('')} />
            )}
            <div style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '0.375rem 1.5rem', marginBottom: '1rem' }}>
              <strong>Status</strong>
              <span>
                <Tag type={detail.status === 'ready' ? 'green' : 'red'}>{detail.status}</Tag>
                {detail.github_configured ? <Tag type="green">GitHub configured</Tag> : <Tag type="gray">GitHub pending</Tag>}
              </span>
              <strong>Namespace</strong> <span>{detail.namespace} (Vault · Nomad · WIF role — immutable)</span>
              <strong>Workspace user</strong> <span>{detail.workspace_user || 'dev'} (baked into SSH role + Boundary library — immutable)</span>
              <strong>Boundary scope</strong> <span>{detail.project_scope_id || '—'}</span>
              <strong>Credential library</strong> <span>{detail.credential_library_id || '—'}</span>
              <strong>Flavors</strong> <span>{detail.flavors?.map((f) => f.name).join(', ') || 'none yet'}</span>
              <strong>Created</strong> <span>{detail.created_by || '—'} · {new Date(detail.created_at).toLocaleString()}</span>
            </div>
            <TextInput
              id="edit-group"
              labelText="IBM Verify developers group (governs portal access — takes effect on next request; running workspaces untouched)"
              value={editGroup}
              onChange={(e) => setEditGroup(e.target.value)}
            />
            <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.75rem', marginTop: '0.5rem' }}>
              Note: the Nomad UI OIDC binding rule still references the group set at create time; direct
              Nomad UI access follows the old group until that rule is updated.
            </p>
            <Button
              kind="danger--ghost"
              size="sm"
              style={{ marginTop: '1.5rem' }}
              onClick={() => {
                setDeleteFor(detail)
                setConfirmName('')
                setDeleteErr('')
                // Close the Details modal so only the confirm modal is open — two stacked
                // Carbon Modals leave the first modal's focus-trap active, which blocks the
                // confirm TextInput from receiving focus/typing until it is reopened.
                setDetail(null)
              }}
            >
              Delete project…
            </Button>
          </>
        )}
      </Modal>

      <Modal
        open={deleteFor !== null}
        danger
        modalHeading={`Delete ${deleteFor?.project_name || ''}?`}
        modalLabel="Irreversible"
        primaryButtonText={busy ? 'Deleting…' : 'Delete project'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={busy || confirmName.trim() !== deleteFor?.project_name}
        onRequestClose={() => setDeleteFor(null)}
        onRequestSubmit={doDelete}
      >
        {deleteErr && (
          <InlineNotification
            kind="error"
            title="Delete incomplete"
            subtitle={`${deleteErr} — the project is kept with status=error; delete again to retry (idempotent).`}
            lowContrast
            onCloseButtonClick={() => setDeleteErr('')}
          />
        )}
        <p style={{ marginBottom: '1rem' }}>
          This tears down <strong>everything</strong> the project owns: all running workspaces and MCP
          servers, its Vault namespace (SSH CA, GitHub App config, secrets), Nomad namespace, Boundary
          scope, gateway registrations, and its LLM virtual key. Workspace home data is destroyed. This
          cannot be undone.
        </p>
        <TextInput
          id="confirm-name"
          labelText={`Type the project name (${deleteFor?.project_name || ''}) to confirm`}
          value={confirmName}
          onChange={(e) => setConfirmName(e.target.value)}
        />
      </Modal>
    </div>
  )
}
