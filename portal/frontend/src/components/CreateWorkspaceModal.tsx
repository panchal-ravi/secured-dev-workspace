import { useState } from 'react'
import { InlineNotification, Modal, RadioTile, TileGroup } from '@carbon/react'
import { createWorkspace, Project, Workspace } from '../api/client'
import FeatureTags from './FeatureTags'

// CreateWorkspaceModal lets the developer pick a TEMPLATE from selectable detail
// cards (one RadioTile per flavor) showing the template's label, purpose, the git
// repo it clones, its base image and its capabilities — so they choose the right
// one. Everything else (name, identity, port, volume) is generated server-side.
export default function CreateWorkspaceModal({
  open,
  project,
  onClose,
  onCreated,
}: {
  open: boolean
  project: Project
  onClose: () => void
  onCreated: (w: Workspace) => void
}) {
  const [flavor, setFlavor] = useState(project.flavors[0]?.name || '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const w = await createWorkspace(project.name, flavor ? { flavor } : {})
      onCreated(w)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const repoLabel = (url?: string) => url?.replace(/^https:\/\/(www\.)?github\.com\//, '').replace(/\.git$/, '')

  return (
    <Modal
      open={open}
      modalHeading="Create workspace"
      primaryButtonText={busy ? 'Creating…' : 'Create'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy || !flavor}
      onRequestClose={onClose}
      onRequestSubmit={submit}
    >
      {err && <InlineNotification kind="error" title="Could not create workspace" subtitle={err} lowContrast />}
      <p style={{ marginBottom: '1rem' }}>
        A new workspace is provisioned with a generated name — your identity, git config, the SSH port and
        the volume are all configured automatically. Choose a template:
      </p>
      <TileGroup
        name="flavor"
        legend="Template"
        valueSelected={flavor}
        onChange={(value) => setFlavor(String(value))}
      >
        {project.flavors.map((f) => (
          <RadioTile key={f.name} id={`flavor-${f.name}`} value={f.name}>
            <div style={{ fontWeight: 600 }}>{f.label || f.name}</div>
            {f.description && (
              <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem', margin: '0.25rem 0' }}>
                {f.description}
              </p>
            )}
            {f.git_repo_url && (
              <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.75rem', margin: 0 }}>
                repo: {repoLabel(f.git_repo_url)}
              </p>
            )}
            {f.image && (
              <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.75rem', margin: 0 }}>image: {f.image}</p>
            )}
            <FeatureTags features={f.features} />
          </RadioTile>
        ))}
      </TileGroup>
    </Modal>
  )
}
