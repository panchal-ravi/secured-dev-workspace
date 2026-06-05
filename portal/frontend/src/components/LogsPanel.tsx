import { useCallback, useEffect, useState } from 'react'
import { ContentSwitcher, IconButton, InlineLoading, InlineNotification, Switch, Toggle } from '@carbon/react'
import { Close, Maximize, Minimize, Renew } from '@carbon/icons-react'
import { fetchLogs, Workspace } from '../api/client'

// LogsPanel is a right-hand slide-over showing the tail of a workspace's
// stdout/stderr from the latest Nomad allocation in a terminal-like window.
// Clicking the overlay (anywhere outside the panel) or pressing Escape closes
// it; it can be maximized to fill the viewport.
export default function LogsPanel({ ws, open, onClose }: { ws: Workspace; open: boolean; onClose: () => void }) {
  const [stream, setStream] = useState<'stdout' | 'stderr'>('stdout')
  const [logs, setLogs] = useState('')
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  const [wrap, setWrap] = useState(false)
  const [maximized, setMaximized] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    setErr('')
    fetchLogs(ws.project, ws.name, stream)
      .then(setLogs)
      .catch((e) => setErr(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false))
  }, [ws.project, ws.name, stream])

  useEffect(() => {
    if (open) load()
  }, [open, load])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  return (
    <div className="logs-overlay" onClick={onClose}>
      <aside
        className={`logs-panel${maximized ? ' logs-panel--max' : ''}`}
        role="dialog"
        aria-label={`Logs for ${ws.workspace_name}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="logs-panel__head">
          <div className="logs-panel__title">
            <strong>{ws.workspace_name}</strong>
            <span className="logs-panel__sub">Logs</span>
          </div>
          <div className="logs-panel__actions">
            <IconButton
              label={maximized ? 'Restore' : 'Maximize'}
              kind="ghost"
              size="sm"
              onClick={() => setMaximized((v) => !v)}
            >
              {maximized ? <Minimize /> : <Maximize />}
            </IconButton>
            <IconButton label="Close" kind="ghost" size="sm" onClick={onClose}>
              <Close />
            </IconButton>
          </div>
        </div>
        <div className="logs-panel__toolbar">
          <ContentSwitcher
            selectedIndex={stream === 'stdout' ? 0 : 1}
            onChange={({ name }) => setStream(name as 'stdout' | 'stderr')}
          >
            <Switch name="stdout" text="stdout" />
            <Switch name="stderr" text="stderr" />
          </ContentSwitcher>
          <div className="logs-panel__toolbar-right">
            <Toggle
              id={`wrap-${ws.name}`}
              size="sm"
              hideLabel
              labelText="Word wrap"
              labelA="Word wrap"
              labelB="Word wrap"
              toggled={wrap}
              onToggle={setWrap}
            />
            {loading ? (
              <InlineLoading description="Loading…" />
            ) : (
              <IconButton label="Refresh" kind="ghost" size="sm" onClick={load}>
                <Renew />
              </IconButton>
            )}
          </div>
        </div>
        {err && (
          <InlineNotification kind="error" title="Could not load logs" subtitle={err} lowContrast hideCloseButton />
        )}
        <pre className={`logs-panel__body${wrap ? ' logs-panel__body--wrap' : ''}`}>
          {logs || (loading ? '' : 'No output.')}
        </pre>
      </aside>
    </div>
  )
}
