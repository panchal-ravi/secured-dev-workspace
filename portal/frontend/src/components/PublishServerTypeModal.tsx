import { useEffect, useState } from 'react'
import { Modal, Select, SelectItem } from '@carbon/react'
import { listBlueprints, publishMcpServer, Blueprint } from '../api/client'

export default function PublishServerTypeModal({
  open,
  serverName,
  onClose,
  onPublished,
}: {
  open: boolean
  serverName: string
  onClose: () => void
  onPublished: () => void
}) {
  const [published, setPublished] = useState<Blueprint[]>([])
  const [choice, setChoice] = useState('') // "" = publish without a blueprint
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    if (!open) return
    setChoice('')
    setErr('')
    listBlueprints()
      .then((bs) => setPublished(bs.filter((b) => b.status === 'published')))
      .catch((e) => setErr((e as Error).message))
  }, [open])

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const bp = published.find((b) => `${b.id}@${b.version}` === choice)
      await publishMcpServer(
        serverName,
        bp ? { id: bp.id, version: bp.version, content_hash: bp.content_hash } : undefined,
      )
      onPublished()
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
      modalHeading="Publish MCP server type"
      modalLabel={serverName}
      primaryButtonText={busy ? 'Publishing…' : 'Publish'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy}
      onRequestClose={onClose}
      onRequestSubmit={submit}
    >
      {err && <p style={{ color: 'var(--cds-text-error)', marginBottom: '1rem' }}>{err}</p>}
      <p style={{ marginBottom: '1rem', color: 'var(--cds-text-secondary)' }}>
        Bind a published credential blueprint to make this type deployable by project-admins (its
        credential is brokered by the blueprint, never pasted). Leave unbound to publish for shared
        platform use only.
      </p>
      <Select id="bind-bp" labelText="Credential blueprint" value={choice} onChange={(e) => setChoice(e.target.value)}>
        <SelectItem value="" text="— none (publish without a blueprint) —" />
        {published.map((b) => (
          <SelectItem key={`${b.id}@${b.version}`} value={`${b.id}@${b.version}`} text={`${b.id} v${b.version} (class ${b.class})`} />
        ))}
      </Select>
    </Modal>
  )
}
