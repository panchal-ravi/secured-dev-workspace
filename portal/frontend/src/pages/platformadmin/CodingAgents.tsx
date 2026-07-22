import { useEffect, useState } from 'react'
import {
  InlineNotification, Loading, Toggle,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
} from '@carbon/react'
import { listCodingAgents, setCodingAgentEnabled, CodingAgent } from '../../api/client'

// CodingAgents is the platform-admin allow-list: which code-known coding agents
// (Claude Code, IBM Bob Shell) project-admins may pick when creating a workspace
// flavor. The agent set is fixed in code (the binaries are baked into the workspace
// image); this page only toggles availability.
export default function CodingAgents() {
  const [agents, setAgents] = useState<CodingAgent[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  const refresh = () =>
    listCodingAgents()
      .then(setAgents)
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  const toggle = async (key: string, enabled: boolean) => {
    setBusy(key)
    setErr('')
    try {
      await setCodingAgentEnabled(key, enabled)
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading coding agents" />

  return (
    <div className="page">
      <div style={{ marginBottom: '1rem' }}>
        <h2>Coding agents</h2>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        The coding agents a project-admin may choose when creating a workspace flavor. Every agent's
        binary is baked into the workspace image, so this is a governance switch — disabling one
        removes it from the flavor picker without touching existing flavors or running workspaces.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      <TableContainer>
        <Table size="lg">
          <TableHead>
            <TableRow>
              <TableHeader>Agent</TableHeader>
              <TableHeader>Governance</TableHeader>
              <TableHeader>Available</TableHeader>
            </TableRow>
          </TableHead>
          <TableBody>
            {agents.map((a) => (
              <TableRow key={a.key}>
                <TableCell>{a.label}</TableCell>
                <TableCell style={{ maxWidth: '32rem', whiteSpace: 'normal' }}>{a.governance || '—'}</TableCell>
                <TableCell>
                  <Toggle
                    id={`agent-${a.key}`}
                    size="sm"
                    hideLabel
                    labelText={`${a.label} available`}
                    labelA="Disabled"
                    labelB="Enabled"
                    toggled={a.enabled}
                    disabled={busy === a.key}
                    onToggle={(checked) => toggle(a.key, checked)}
                  />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
    </div>
  )
}
