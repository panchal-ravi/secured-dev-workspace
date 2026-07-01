import { useEffect, useState } from 'react'
import {
  Button,
  InlineLoading,
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
  deleteMcpServer,
  McpServer,
} from '../../api/client'
import DeployMcpServerModal from '../../components/DeployMcpServerModal'
import PublishServerTypeModal from '../../components/PublishServerTypeModal'

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
  const [busyLabel, setBusyLabel] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<McpServer | null>(null)
  const [publishFor, setPublishFor] = useState('')

  const refresh = () =>
    listMcpServers()
      .then(setServers)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
  }, [])

  const run = async (name: string, label: string, fn: () => Promise<unknown>) => {
    setBusy(name)
    setBusyLabel(label)
    setErr('')
    try {
      await fn()
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
      setBusyLabel('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading MCP servers" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>MCP servers</h2>
        <Button
          renderIcon={Add}
          onClick={() => {
            setEditing(null)
            setModalOpen(true)
          }}
        >
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
                      {busy === s.name ? (
                        <InlineLoading
                          status="active"
                          description={
                            busyLabel === 'Testing'
                              ? 'Running consumption-mirror test — can take up to a minute…'
                              : `${busyLabel}…`
                          }
                        />
                      ) : (
                        <div style={{ display: 'flex', gap: '0.5rem' }}>
                          <Button
                            size="sm"
                            kind="ghost"
                            onClick={() => {
                              setEditing(s)
                              setModalOpen(true)
                            }}
                          >
                            Edit
                          </Button>
                          <Button
                            size="sm"
                            kind="tertiary"
                            onClick={() => run(s.name, 'Testing', () => testMcpServer(s.name))}
                          >
                            Test
                          </Button>
                          <Button
                            size="sm"
                            kind="primary"
                            disabled={!tested || s.status === 'published'}
                            onClick={() => setPublishFor(s.name)}
                          >
                            Publish
                          </Button>
                          <Button
                            size="sm"
                            kind="danger--ghost"
                            onClick={() => run(s.name, 'Deleting', () => deleteMcpServer(s.name))}
                          >
                            Delete
                          </Button>
                        </div>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      <DeployMcpServerModal
        open={modalOpen}
        initial={editing}
        onClose={() => {
          setModalOpen(false)
          setEditing(null)
        }}
        onDeployed={refresh}
      />
      <PublishServerTypeModal
        open={publishFor !== ''}
        serverName={publishFor}
        onClose={() => setPublishFor('')}
        onPublished={refresh}
      />
    </div>
  )
}
