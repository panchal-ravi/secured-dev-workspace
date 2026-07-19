import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button,
  InlineLoading,
  InlineNotification,
  Loading,
  Modal,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
  Toggle,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import {
  createSharedVolume,
  deleteSharedVolume,
  listSharedVolumes,
  SharedVolume,
} from '../../api/client'

// SharedVolumes is the project-admin management page for per-project shared EFS
// volumes (package/build caches, datasets). A volume created here shows up
// pre-selected on the workspace-create modal, so developers get it by default.
export default function SharedVolumes() {
  const { name = '' } = useParams()
  const [volumes, setVolumes] = useState<SharedVolume[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  const [modalOpen, setModalOpen] = useState(false)
  const [modalErr, setModalErr] = useState('')
  const [form, setForm] = useState({ name: '', mountPath: '', readOnly: false })
  const set = (patch: Partial<typeof form>) => setForm((f) => ({ ...f, ...patch }))

  const refresh = () => {
    setLoading(true)
    listSharedVolumes(name)
      .then((v) => setVolumes(v || []))
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }
  useEffect(refresh, [name])

  const submit = async () => {
    setBusy('create')
    setModalErr('')
    try {
      await createSharedVolume(name, {
        name: form.name.trim(),
        mount_path: form.mountPath.trim() || undefined,
        read_only: form.readOnly,
      })
      setModalOpen(false)
      setForm({ name: '', mountPath: '', readOnly: false })
      refresh()
    } catch (e) {
      setModalErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  const remove = async (v: SharedVolume) => {
    setBusy(`del:${v.name}`)
    setErr('')
    try {
      await deleteSharedVolume(name, v.name)
      refresh()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading shared volumes" />

  return (
    <div style={{ padding: '1.5rem', maxWidth: '60rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h2>Shared volumes</h2>
        <Button renderIcon={Add} onClick={() => setModalOpen(true)}>
          Create volume
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0.5rem 0 1.5rem' }}>
        Shared EFS volumes mount into every workspace in this project (developers can opt out per workspace).
        Use them for package/build caches (mount at <code>/shared/cache</code> to auto-warm Go/npm/pip caches)
        or datasets shared across workspaces.
      </p>

      {err && (
        <InlineNotification
          kind="error"
          title="Error"
          subtitle={err}
          lowContrast
          onCloseButtonClick={() => setErr('')}
        />
      )}

      {volumes.length === 0 ? (
        <p style={{ color: 'var(--cds-text-secondary)' }}>None yet. Create one to share caches or datasets.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Mount path</TableHeader>
                <TableHeader>Access</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {volumes.map((v) => (
                <TableRow key={v.name}>
                  <TableCell>{v.name}</TableCell>
                  <TableCell>
                    <code>{v.mount_path}</code>
                  </TableCell>
                  <TableCell>
                    <Tag type={v.read_only ? 'cool-gray' : 'green'}>{v.read_only ? 'read-only' : 'read-write'}</Tag>
                  </TableCell>
                  <TableCell>
                    {busy === `del:${v.name}` ? (
                      <InlineLoading description="Deleting…" />
                    ) : (
                      <Button size="sm" kind="danger--ghost" disabled={busy !== ''} onClick={() => remove(v)}>
                        Delete
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Modal
        open={modalOpen}
        modalHeading="Create shared volume"
        modalLabel={name}
        primaryButtonText={busy === 'create' ? 'Creating…' : 'Create'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!form.name.trim() || busy === 'create'}
        onRequestClose={() => setModalOpen(false)}
        onRequestSubmit={submit}
      >
        {modalErr && <InlineNotification kind="error" title="Could not create volume" subtitle={modalErr} lowContrast />}
        <Stack gap={5}>
          <TextInput
            id="sv-name"
            labelText="Name"
            helperText="Lowercase letters, digits, dash (2–31 chars). Used in the volume id."
            placeholder="cache"
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
          />
          <TextInput
            id="sv-mount"
            labelText="Mount path"
            helperText="Where it mounts in the workspace. Defaults to /shared/<name>. Use /shared/cache to auto-warm package caches."
            placeholder="/shared/cache"
            value={form.mountPath}
            onChange={(e) => set({ mountPath: e.target.value })}
          />
          <Toggle
            id="sv-readonly"
            labelText="Access"
            labelA="Read-write"
            labelB="Read-only"
            toggled={form.readOnly}
            onToggle={(checked: boolean) => set({ readOnly: checked })}
          />
        </Stack>
      </Modal>
    </div>
  )
}
