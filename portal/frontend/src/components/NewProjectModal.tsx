import { useState } from 'react'
import { Modal, Stack, TextInput } from '@carbon/react'
import { createProject } from '../api/client'

// NewProjectModal drives POST /api/admin/projects: it bootstraps the project's
// Vault namespace + Nomad namespace + Boundary scope + descriptor and grants the
// first project-admin. Per-project engines (SSH/GitHub/DB) are set up afterward
// from the project's own admin view.
export default function NewProjectModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [projectName, setProjectName] = useState('')
  const [group, setGroup] = useState('')
  const [workspaceUser, setWorkspaceUser] = useState('dev')
  const [firstAdmin, setFirstAdmin] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await createProject({
        project_name: projectName.trim(),
        developers_group_name: group.trim(),
        workspace_user: workspaceUser.trim() || 'dev',
        first_admin: firstAdmin.trim(),
      })
      onCreated()
      onClose()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      modalHeading="New project"
      modalLabel="Project"
      primaryButtonText={busy ? 'Creating…' : 'Create project'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy || !projectName.trim() || !group.trim() || !firstAdmin.trim()}
      onRequestClose={onClose}
      onRequestSubmit={submit}
      size="md"
    >
      {err && <p style={{ color: 'var(--cds-text-error)', marginBottom: '1rem' }}>{err}</p>}
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Creates the project&apos;s Vault namespace, Nomad namespace, and Boundary scope, then grants
        the first project-admin. That admin must already belong to the developers group in IBM Verify.
      </p>
      <Stack gap={5}>
        <TextInput
          id="np-name"
          labelText="Project name"
          helperText="lowercase letters, digits, hyphens (1–63 chars)"
          placeholder="project-acme"
          value={projectName}
          onChange={(e) => setProjectName(e.target.value)}
        />
        <TextInput
          id="np-group"
          labelText="Developers group (IBM Verify)"
          placeholder="project-acme-developers"
          value={group}
          onChange={(e) => setGroup(e.target.value)}
        />
        <TextInput
          id="np-user"
          labelText="Workspace user"
          value={workspaceUser}
          onChange={(e) => setWorkspaceUser(e.target.value)}
        />
        <TextInput
          id="np-admin"
          labelText="First project-admin (email)"
          placeholder="admin@example.com"
          value={firstAdmin}
          onChange={(e) => setFirstAdmin(e.target.value)}
        />
      </Stack>
    </Modal>
  )
}
