import { useEffect, useState } from 'react'
import { Checkbox, InlineNotification, Modal, RadioTile, TileGroup } from '@carbon/react'
import { createWorkspace, listSharedVolumes, Project, SharedVolume, Workspace } from '../api/client'
import FeatureTags from './FeatureTags'

// CreateWorkspaceModal lets the developer pick a TEMPLATE from selectable detail
// cards (one RadioTile per flavor) showing the template's label, purpose, the git
// repo it clones, its base image and its capabilities — so they choose the right
// one. Everything else (name, identity, port, volume) is generated server-side.
// Any shared volumes the project-admin created are listed pre-checked, so the
// developer gets them by default but can opt out.
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
  const [volumes, setVolumes] = useState<SharedVolume[]>([])
  const [selected, setSelected] = useState<string[]>([])

  // Load the project's shared volumes when the modal opens and pre-select them all.
  // A missing endpoint (admin plane off) or an empty list simply shows no volumes.
  useEffect(() => {
    if (!open) return
    listSharedVolumes(project.name)
      .then((vs) => {
        setVolumes(vs || [])
        setSelected((vs || []).map((v) => v.name))
      })
      .catch(() => {
        setVolumes([])
        setSelected([])
      })
  }, [open, project.name])

  const toggle = (name: string, checked: boolean) =>
    setSelected((s) => (checked ? [...new Set([...s, name])] : s.filter((n) => n !== name)))

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const body: { flavor?: string; shared_volumes?: string[] } = {}
      if (flavor) body.flavor = flavor
      if (selected.length) body.shared_volumes = selected
      const w = await createWorkspace(project.name, body)
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

      {volumes.length > 0 && (
        <fieldset style={{ border: 0, padding: 0, margin: '1.25rem 0 0' }}>
          <legend style={{ fontWeight: 600, marginBottom: '0.25rem' }}>Shared volumes</legend>
          <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem', margin: '0 0 0.5rem' }}>
            Mounted into this workspace for shared caches and datasets. Pre-selected — uncheck any you don’t want.
          </p>
          {volumes.map((v) => (
            <Checkbox
              key={v.name}
              id={`sv-${v.name}`}
              labelText={`${v.name} → ${v.mount_path}${v.read_only ? ' (read-only)' : ''}`}
              checked={selected.includes(v.name)}
              onChange={(_e: unknown, { checked }: { checked: boolean }) => toggle(v.name, checked)}
            />
          ))}
        </fieldset>
      )}
    </Modal>
  )
}
