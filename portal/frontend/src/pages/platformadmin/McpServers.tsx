import { useEffect, useState } from 'react'
import {
  Button,
  InlineNotification,
  Loading,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import {
  listMcpServers,
  testMcpServer,
  publishMcpServer,
  deleteMcpServer,
  McpServer,
} from '../../api/client'
import DeployMcpServerModal from '../../components/DeployMcpServerModal'

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = {
  draft: 'gray',
  deployed: 'blue',
  published: 'green',
}

export default function McpServers() {
  const [servers, setServers] = useState<McpServer[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [modalOpen, setModalOpen] = useState(false)

  const refresh = () =>
    listMcpServers()
      .then(setServers)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  const run = async (name: string, fn: () => Promise<unknown>) => {
    setBusy(name)
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

  if (loading) return <Loading withOverlay description="Loading MCP servers" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>MCP servers</h2>
        <Button renderIcon={Add} onClick={() => setModalOpen(true)}>
          Deploy MCP server
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Existing MCP servers deployed on the platform. Deploy a new one, verify it the
        way a project will consume it (a scoped virtual-server token), then publish.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {servers.length === 0 ? (
        <p>No MCP servers deployed yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Image</TableHeader>
                <TableHeader>Transport</TableHeader>
                <TableHeader>Port</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Test</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {servers.map((s) => {
                const tested = s.test_result?.passed
                return (
                  <TableRow key={s.name}>
                    <TableCell>{s.name}</TableCell>
                    <TableCell>{s.image}</TableCell>
                    <TableCell>{s.transport}</TableCell>
                    <TableCell>{s.port}</TableCell>
                    <TableCell>
                      <Tag type={statusTag[s.status] || 'gray'}>{s.status}</Tag>
                    </TableCell>
                    <TableCell>
                      {s.test_result ? (
                        <Tag type={tested ? 'green' : 'red'}>
                          {tested ? `passed (${s.test_result.tools_discovered} tools)` : 'failed'}
                        </Tag>
                      ) : (
                        <Tag type="gray">untested</Tag>
                      )}
                    </TableCell>
                    <TableCell>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
                        <Button
                          size="sm"
                          kind="tertiary"
                          disabled={busy === s.name}
                          onClick={() => run(s.name, () => testMcpServer(s.name))}
                        >
                          Test
                        </Button>
                        <Button
                          size="sm"
                          kind="primary"
                          disabled={busy === s.name || !tested || s.status === 'published'}
                          onClick={() => run(s.name, () => publishMcpServer(s.name))}
                        >
                          Publish
                        </Button>
                        <Button
                          size="sm"
                          kind="danger--ghost"
                          disabled={busy === s.name}
                          onClick={() => run(s.name, () => deleteMcpServer(s.name))}
                        >
                          Delete
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
      <DeployMcpServerModal open={modalOpen} onClose={() => setModalOpen(false)} onDeployed={refresh} />
    </div>
  )
}
