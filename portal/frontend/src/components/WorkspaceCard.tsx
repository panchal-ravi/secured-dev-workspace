import { useState } from 'react'
import { Button, InlineNotification, Modal, Tag, Tile } from '@carbon/react'
import { destroyWorkspace, startWorkspace, stopWorkspace, Workspace } from '../api/client'
import ConnectTabs from './ConnectTabs'
import FeatureTags from './FeatureTags'
import LogsPanel from './LogsPanel'

export default function WorkspaceCard({ ws, onChanged }: { ws: Workspace; onChanged?: () => void }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [logsOpen, setLogsOpen] = useState(false)

  const statusType = ws.status === 'running' ? 'green' : ws.status === 'pending' ? 'teal' : 'gray'

  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setErr('')
    try {
      await fn()
      onChanged?.()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Tile>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <h4>{ws.workspace_name}</h4>
        <Tag type={statusType}>{ws.status || 'unknown'}</Tag>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem' }}>
        port {ws.port} · {ws.target_id || 'target pending'}
      </p>
      {err && (
        <InlineNotification kind="error" title="Action failed" subtitle={err} lowContrast onClose={() => setErr('')} />
      )}
      <FeatureTags features={ws.features} />
      <ConnectTabs ws={ws} />
      <div style={{ display: 'flex', gap: '0.5rem', marginTop: '1rem' }}>
        {ws.status === 'running' && (
          <Button size="sm" kind="secondary" disabled={busy} onClick={() => run(() => stopWorkspace(ws.project, ws.name))}>
            Stop
          </Button>
        )}
        {ws.status === 'dead' && (
          <Button size="sm" kind="secondary" disabled={busy} onClick={() => run(() => startWorkspace(ws.project, ws.name))}>
            Start
          </Button>
        )}
        <Button size="sm" kind="ghost" onClick={() => setLogsOpen(true)}>
          Logs
        </Button>
        <Button size="sm" kind="danger--tertiary" disabled={busy} onClick={() => setConfirmOpen(true)}>
          Destroy
        </Button>
      </div>
      <LogsPanel ws={ws} open={logsOpen} onClose={() => setLogsOpen(false)} />
      <Modal
        open={confirmOpen}
        danger
        modalHeading="Destroy workspace"
        primaryButtonText="Destroy"
        secondaryButtonText="Cancel"
        onRequestClose={() => setConfirmOpen(false)}
        onRequestSubmit={() => {
          setConfirmOpen(false)
          run(() => destroyWorkspace(ws.project, ws.name))
        }}
      >
        <p>
          This permanently deletes workspace <strong>{ws.workspace_name}</strong>, including its home volume and all
          Boundary access. This cannot be undone.
        </p>
      </Modal>
    </Tile>
  )
}
