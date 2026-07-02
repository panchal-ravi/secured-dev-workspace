import { useEffect, useState } from 'react'
import {
  Button, InlineNotification, Loading, Tag, Modal, TextArea, TextInput,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
} from '@carbon/react'
import {
  listBaseTemplates, updateBaseTemplateDraft, publishBaseTemplate, BaseJobTemplate,
} from '../../api/client'

const statusTag: Record<string, 'gray' | 'green'> = {
  draft: 'gray',
  published: 'green',
}

export default function BaseTemplates() {
  const [tmpls, setTmpls] = useState<BaseJobTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  // Edit modal state.
  const [editing, setEditing] = useState<BaseJobTemplate | null>(null)
  const [source, setSource] = useState('')
  const [image, setImage] = useState('')
  const [modalErr, setModalErr] = useState('')
  const [saving, setSaving] = useState(false)

  const refresh = () =>
    listBaseTemplates()
      .then(setTmpls)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key)
    setErr('')
    try {
      await fn()
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const openEdit = (t: BaseJobTemplate) => {
    setEditing(t)
    setSource(t.draft_source)
    setImage(t.image || '')
    setModalErr('')
  }

  const saveDraft = async () => {
    if (!editing) return
    setSaving(true)
    setModalErr('')
    try {
      await updateBaseTemplateDraft(editing.name, source, image)
      setEditing(null)
      await refresh()
    } catch (e) {
      setModalErr((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <Loading withOverlay description="Loading base job templates" />

  return (
    <div className="page">
      <div style={{ marginBottom: '1rem' }}>
        <h2>Base workspace templates</h2>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Platform-authored generic Nomad job templates (standard, GPU, microVM) available to every
        project. Edit the template source — it carries <code>${'{...}'}</code> placeholders filled in
        at project-template create (project-static) and workspace launch (per-workspace) — then
        publish to cut a new version. Project templates snapshot the published source at create time,
        so editing here never disturbs live projects.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {tmpls.length === 0 ? (
        <p>No base templates.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Label</TableHeader>
                <TableHeader>Image</TableHeader>
                <TableHeader>Node pool</TableHeader>
                <TableHeader>Version</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {tmpls.map((t) => (
                <TableRow key={t.name}>
                  <TableCell>{t.name}</TableCell>
                  <TableCell>{t.label || '—'}</TableCell>
                  <TableCell>{t.image || '—'}</TableCell>
                  <TableCell>{t.default_node_pool || 'default'}</TableCell>
                  <TableCell>{t.version}</TableCell>
                  <TableCell>
                    <Tag type={statusTag[t.status] || 'gray'}>{t.status}</Tag>
                  </TableCell>
                  <TableCell>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                      <Button size="sm" kind="tertiary" disabled={busy === t.name} onClick={() => openEdit(t)}>
                        Edit
                      </Button>
                      <Button
                        size="sm"
                        kind="primary"
                        disabled={busy === t.name}
                        onClick={() => run(t.name, () => publishBaseTemplate(t.name))}
                      >
                        Publish
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {editing && (
        <Modal
          open
          modalHeading={`Edit ${editing.name}`}
          primaryButtonText={saving ? 'Saving…' : 'Save draft'}
          secondaryButtonText="Cancel"
          primaryButtonDisabled={saving}
          onRequestClose={() => setEditing(null)}
          onRequestSubmit={saveDraft}
          size="lg"
        >
          <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
            Saving validates that every <code>${'{...}'}</code> placeholder is one of the known
            names; unknown placeholders are rejected. Saving marks the template as draft — click
            Publish to release a new version.
          </p>
          {modalErr && (
            <InlineNotification
              kind="error"
              title="Validation failed"
              subtitle={modalErr}
              lowContrast
              onCloseButtonClick={() => setModalErr('')}
            />
          )}
          <TextInput
            id="base-image"
            labelText="Container image (baked into project templates; project-admins see it readonly)"
            placeholder="panchalravi/dev-workspace:poc"
            value={image}
            onChange={(e) => setImage(e.target.value)}
            style={{ marginBottom: '1rem' }}
          />
          <TextArea
            labelText="Template source (Nomad HCL)"
            value={source}
            rows={24}
            onChange={(e) => setSource(e.target.value)}
            style={{ fontFamily: 'monospace' }}
          />
        </Modal>
      )}
    </div>
  )
}
