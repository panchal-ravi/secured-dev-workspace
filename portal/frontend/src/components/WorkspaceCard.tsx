import { useState } from 'react'
import { Button, InlineNotification, Loading, Modal, Tag, TextInput, Tile } from '@carbon/react'
import { destroyWorkspace, disconnectLink, startWorkspace, stopWorkspace, Workspace } from '../api/client'
import ConnectTabs from './ConnectTabs'
import FeatureTags from './FeatureTags'
import LogsPanel from './LogsPanel'

export default function WorkspaceCard({ ws, onChanged }: { ws: Workspace; onChanged?: () => void }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [stopConfirmOpen, setStopConfirmOpen] = useState(false)
  const [destroyConfirmText, setDestroyConfirmText] = useState('')
  const [logsOpen, setLogsOpen] = useState(false)

  const destroyConfirmed = destroyConfirmText.trim() === ws.workspace_name

  const statusType =
    ws.status === 'running'
      ? 'green'
      : ws.status === 'pending'
        ? 'teal'
        : ws.status === 'failed' || ws.status === 'lost'
          ? 'red'
          : 'gray'

  // Nomad reports a stopped job as "dead"; show the developer the friendlier "stopped".
  const statusLabel = ws.status === 'dead' ? 'stopped' : ws.status || 'unknown'

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
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
          {ws.status === 'pending' && (
            <Loading small withOverlay={false} description="Workspace starting" />
          )}
          <Tag type={statusType}>{statusLabel}</Tag>
        </div>
      </div>
      {ws.flavor && (
        <div style={{ marginTop: '0.25rem' }}>
          <Tag type="cool-gray" size="sm">
            {ws.flavor}
          </Tag>
        </div>
      )}
      <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem' }}>
        port {ws.port} · {ws.target_id || 'target pending'}
      </p>
      {err && (
        <InlineNotification kind="error" title="Action failed" subtitle={err} lowContrast onClose={() => setErr('')} />
      )}
      <FeatureTags features={ws.features} />
      <ConnectTabs ws={ws} />
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', marginTop: '1rem' }}>
        {ws.status === 'running' && (
          <Button size="sm" kind="secondary" disabled={busy} onClick={() => setStopConfirmOpen(true)}>
            Stop
          </Button>
        )}
        {ws.status === 'dead' && (
          <Button size="sm" kind="secondary" disabled={busy} onClick={() => run(() => startWorkspace(ws.project, ws.name))}>
            Start
          </Button>
        )}
        <Button size="sm" kind="tertiary" onClick={() => setLogsOpen(true)}>
          Logs
        </Button>
        <Button size="sm" kind="danger--tertiary" disabled={busy} onClick={() => setConfirmOpen(true)}>
          Destroy
        </Button>
      </div>
      <LogsPanel ws={ws} open={logsOpen} onClose={() => setLogsOpen(false)} />
      <Modal
        open={stopConfirmOpen}
        modalHeading="Stop workspace"
        primaryButtonText="Stop"
        secondaryButtonText="Cancel"
        onRequestClose={() => setStopConfirmOpen(false)}
        onRequestSubmit={() => {
          setStopConfirmOpen(false)
          run(() => stopWorkspace(ws.project, ws.name))
        }}
      >
        <p>
          This stops workspace <strong>{ws.workspace_name}</strong>. Its home volume and Boundary access are kept,
          and you can start it again later.
        </p>
      </Modal>
      <Modal
        open={confirmOpen}
        danger
        modalHeading="Destroy workspace"
        primaryButtonText="Destroy"
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!destroyConfirmed}
        onRequestClose={() => {
          setConfirmOpen(false)
          setDestroyConfirmText('')
        }}
        onRequestSubmit={() => {
          if (!destroyConfirmed) return
          setConfirmOpen(false)
          setDestroyConfirmText('')
          run(async () => {
            await destroyWorkspace(ws.project, ws.name)
            // Ask the local helper to drop this workspace's ~/.ssh/config block so
            // it doesn't linger after the workspace is gone. Custom-scheme assign
            // hands off to the helper without navigating away; no-ops if uninstalled
            // or if the block was never written.
            window.location.assign(disconnectLink(ws.name))
          })
        }}
      >
        <p>
          This permanently deletes workspace <strong>{ws.workspace_name}</strong>, including its home volume and all
          Boundary access. This cannot be undone.
        </p>
        <TextInput
          id={`destroy-confirm-${ws.name}`}
          labelText={`Type "${ws.workspace_name}" to confirm`}
          placeholder={ws.workspace_name}
          value={destroyConfirmText}
          onChange={(e) => setDestroyConfirmText(e.target.value)}
          style={{ marginTop: '1rem' }}
        />
      </Modal>
    </Tile>
  )
}
