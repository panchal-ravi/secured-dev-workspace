import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button, InlineNotification, Loading, Modal, Select, SelectItem, TextInput, Tag,
  FormGroup, MultiSelect,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import {
  listProjectMcp, deployProjectMcp, testProjectMcp, deleteProjectMcp,
  DeployableType, ProjectMcpCatalog, PathGrant,
} from '../../api/client'

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = { deployed: 'blue', published: 'green' }
// Capabilities a project-admin may attach to a path grant (sudo/root rejected server-side).
const GRANT_CAPS = ['read', 'list', 'create', 'update', 'delete']
const DEFAULT_CAPS = ['read', 'list']

export default function McpServers() {
  const { name = '' } = useParams()
  const [cat, setCat] = useState<ProjectMcpCatalog>({ deployable: [], deployed: [] })
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [picked, setPicked] = useState<DeployableType | null>(null)
  const [params, setParams] = useState<Record<string, string>>({})
  const [grants, setGrants] = useState<PathGrant[]>([])

  const refresh = () =>
    listProjectMcp(name).then(setCat).catch((e) => setErr(e.message)).finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

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

  const openDeploy = () => {
    setPicked(cat.deployable[0] || null)
    setParams({})
    setGrants([])
    setErr('')
    setModalOpen(true)
  }

  const addGrant = () => setGrants((g) => [...g, { path: '', capabilities: [...DEFAULT_CAPS] }])
  const setGrant = (i: number, patch: Partial<PathGrant>) =>
    setGrants((g) => g.map((row, j) => (j === i ? { ...row, ...patch } : row)))
  const removeGrant = (i: number) => setGrants((g) => g.filter((_, j) => j !== i))

  const submitDeploy = async () => {
    if (!picked) return
    const extra = grants
      .filter((g) => g.path.trim() && g.capabilities.length > 0)
      .map((g) => ({ path: g.path.trim(), capabilities: g.capabilities }))
    await run('deploy', () =>
      deployProjectMcp(name, picked.name, params, extra).then(() => setModalOpen(false)),
    )
  }

  if (loading) return <Loading withOverlay description="Loading MCP servers" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>{name} — MCP servers</h2>
        <Button renderIcon={Add} disabled={cat.deployable.length === 0} onClick={openDeploy}>
          Deploy
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Deploy a published MCP server type into this project. Its credential is provisioned by the
        bound Vault blueprint into the project&apos;s Vault namespace — never pasted.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}

      {cat.deployed.length === 0 ? (
        <p>No MCP servers deployed yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Running</TableHeader>
                <TableHeader>Test</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {cat.deployed.map((s) => {
                const tested = s.test_result?.passed
                return (
                  <TableRow key={s.name}>
                    <TableCell>{s.name}</TableCell>
                    <TableCell>
                      <Tag type={statusTag[s.status] || 'gray'}>{s.status}</Tag>
                    </TableCell>
                    <TableCell>
                      <Tag type={s.running ? 'green' : 'red'}>{s.running ? 'running' : 'stopped'}</Tag>
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
                        <Button size="sm" kind="tertiary" disabled={busy === s.name} onClick={() => run(s.name, () => testProjectMcp(name, s.name))}>
                          Test
                        </Button>
                        <Button size="sm" kind="danger--ghost" disabled={busy === s.name} onClick={() => run(s.name, () => deleteProjectMcp(name, s.name))}>
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

      <Modal
        open={modalOpen}
        modalHeading="Deploy MCP server"
        modalLabel={name}
        primaryButtonText={busy === 'deploy' ? 'Deploying…' : 'Deploy'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!picked || busy === 'deploy'}
        onRequestClose={() => setModalOpen(false)}
        onRequestSubmit={submitDeploy}
      >
        <Select
          id="server-type"
          labelText="Server type"
          value={picked?.name || ''}
          onChange={(e) => {
            setPicked(cat.deployable.find((d) => d.name === e.target.value) || null)
            setParams({})
            setGrants([])
          }}
        >
          {cat.deployable.map((d) => (
            <SelectItem key={d.name} value={d.name} text={`${d.name} (${d.image})`} />
          ))}
        </Select>
        {picked?.params.map((p) => (
          <TextInput
            key={p.name}
            id={`param-${p.name}`}
            labelText={p.prompt || p.name}
            type={p.type === 'secret' ? 'password' : 'text'}
            style={{ marginTop: '1rem' }}
            value={params[p.name] || ''}
            onChange={(e) => setParams((m) => ({ ...m, [p.name]: e.target.value }))}
          />
        ))}
        {picked?.allow_extra_grants && (
          <FormGroup legendText="Additional Vault path grants" style={{ marginTop: '1.5rem' }}>
            <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '0.75rem', fontSize: '0.8rem' }}>
              Grant this server&apos;s token access to other engines in this project&apos;s Vault
              namespace, e.g. <code>pki/{name}/issue/&lt;role&gt;</code>. The engine must already be
              mounted. sudo/root are rejected.
            </p>
            {grants.map((g, i) => (
              <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '0.5rem' }}>
                <TextInput
                  id={`grant-path-${i}`}
                  labelText="Path"
                  placeholder={`pki/${name}/issue/web`}
                  value={g.path}
                  onChange={(e) => setGrant(i, { path: e.target.value })}
                />
                <MultiSelect
                  id={`grant-caps-${i}`}
                  titleText="Capabilities"
                  label={g.capabilities.join(', ') || 'Select'}
                  items={GRANT_CAPS}
                  selectedItems={g.capabilities}
                  onChange={({ selectedItems }) => setGrant(i, { capabilities: selectedItems || [] })}
                />
                <Button hasIconOnly renderIcon={TrashCan} iconDescription="Remove" kind="danger--ghost" size="md" onClick={() => removeGrant(i)} />
              </div>
            ))}
            <Button renderIcon={Add} kind="ghost" size="sm" onClick={addGrant}>
              Add path grant
            </Button>
          </FormGroup>
        )}
      </Modal>
    </div>
  )
}
