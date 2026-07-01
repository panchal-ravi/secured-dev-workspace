import { useEffect, useState } from 'react'
import {
  Button, InlineNotification, Loading, Tag,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import { listBlueprints, validateBlueprint, publishBlueprint, Blueprint } from '../../api/client'
import NewBlueprintModal from '../../components/NewBlueprintModal'

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = {
  draft: 'gray',
  validated: 'blue',
  published: 'green',
}

export default function Blueprints() {
  const [bps, setBps] = useState<Blueprint[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [modalOpen, setModalOpen] = useState(false)

  const refresh = () =>
    listBlueprints()
      .then(setBps)
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

  if (loading) return <Loading withOverlay description="Loading Vault blueprints" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>Vault credential blueprints</h2>
        <Button renderIcon={Add} onClick={() => setModalOpen(true)}>
          New Vault blueprint
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Platform-authored Vault credential recipes. Author a draft, validate it (lint + a live
        consumption-mirror in a throwaway namespace), then publish so it can be bound to an MCP
        server type. Vault blueprints are immutable — a change is a new version.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {bps.length === 0 ? (
        <p>No Vault blueprints yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>ID</TableHeader>
                <TableHeader>Version</TableHeader>
                <TableHeader>Class</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Validation</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {bps.map((b) => {
                const key = `${b.id}@${b.version}`
                const passed = b.validation?.passed
                return (
                  <TableRow key={key}>
                    <TableCell>{b.id}</TableCell>
                    <TableCell>{b.version}</TableCell>
                    <TableCell>{b.class}</TableCell>
                    <TableCell>
                      <Tag type={statusTag[b.status] || 'gray'}>{b.status}</Tag>
                    </TableCell>
                    <TableCell>
                      {b.validation ? (
                        <div>
                          <Tag type={passed ? 'green' : 'red'}>{passed ? 'passed' : 'failed'}</Tag>
                          {!passed && (
                            <ul style={{ margin: '0.5rem 0 0', fontSize: '0.8rem', color: 'var(--cds-text-secondary)' }}>
                              {(b.validation.checks || [])
                                .filter((c) => !c.passed)
                                .map((c) => (
                                  <li key={c.name}>
                                    {c.name}: {c.detail || 'failed'}
                                  </li>
                                ))}
                            </ul>
                          )}
                        </div>
                      ) : (
                        <Tag type="gray">unvalidated</Tag>
                      )}
                    </TableCell>
                    <TableCell>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
                        <Button
                          size="sm"
                          kind="tertiary"
                          disabled={busy === key || b.status === 'published'}
                          onClick={() => run(key, () => validateBlueprint(b.id, b.version))}
                        >
                          Validate
                        </Button>
                        <Button
                          size="sm"
                          kind="primary"
                          disabled={busy === key || !passed || b.status === 'published'}
                          onClick={() => run(key, () => publishBlueprint(b.id, b.version))}
                        >
                          Publish
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <NewBlueprintModal open={modalOpen} onClose={() => setModalOpen(false)} onCreated={refresh} />
    </div>
  )
}
