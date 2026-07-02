import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button, InlineNotification, Loading, Modal, Select, SelectItem, TextInput, Tag, Checkbox,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import {
  listProjectTemplates, listProjectBaseTemplates, createProjectTemplate, deleteProjectTemplate,
  updateTemplateAddons, listProjectMcp,
  BaseOption, ProjectTemplate, CreateProjectTemplateInput, AddonEngine, ProjectMcpServer,
} from '../../api/client'

// engineRow is the flattened editor shape (one secret file per engine row keeps the
// UI simple; it maps to AddonEngine.secret_files with a single entry).
interface engineRow {
  mount: string
  type: string
  kv_path: string
  kv_field: string
  dest_file: string
}

export default function Templates() {
  const { name = '' } = useParams()
  const [bases, setBases] = useState<BaseOption[]>([])
  const [tmpls, setTmpls] = useState<ProjectTemplate[]>([])
  const [deployedMcp, setDeployedMcp] = useState<ProjectMcpServer[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [form, setForm] = useState<CreateProjectTemplateInput>({ base: '', git_repo_url: '' })
  // Add-ons editor state (per flavor).
  const [addonsFor, setAddonsFor] = useState<ProjectTemplate | null>(null)
  const [mcpSel, setMcpSel] = useState<string[]>([])
  const [engineRows, setEngineRows] = useState<engineRow[]>([])

  const selectedBase = bases.find((b) => b.name === form.base)

  const refresh = () =>
    Promise.all([listProjectBaseTemplates(name), listProjectTemplates(name), listProjectMcp(name)])
      .then(([b, t, mcp]) => {
        setBases(b)
        setTmpls(t)
        setDeployedMcp(mcp.deployed || [])
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false))

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

  const openCreate = () => {
    const first = bases[0]
    setForm({ base: first?.name || '', git_repo_url: '', node_pool: first?.default_node_pool })
    setErr('')
    setModalOpen(true)
  }

  const submit = () =>
    run('create', () =>
      createProjectTemplate(name, {
        ...form,
        base: form.base.trim(),
        git_repo_url: form.git_repo_url.trim(),
      }).then(() => setModalOpen(false)),
    )

  const openAddons = (t: ProjectTemplate) => {
    setAddonsFor(t)
    setMcpSel(t.addons?.mcp_servers || [])
    setEngineRows(
      (t.addons?.engines || []).map((e) => ({
        mount: e.mount,
        type: e.type,
        kv_path: e.kv_path || '',
        kv_field: e.secret_files?.[0]?.kv_field || '',
        dest_file: e.secret_files?.[0]?.dest_file || '',
      })),
    )
    setErr('')
  }

  const saveAddons = () => {
    if (!addonsFor) return
    const engines: AddonEngine[] = engineRows
      .filter((r) => r.mount.trim() && r.type.trim())
      .map((r) => ({
        mount: r.mount.trim(),
        type: r.type.trim(),
        kv_path: r.kv_path.trim() || undefined,
        secret_files:
          r.kv_field.trim() && r.dest_file.trim()
            ? [{ kv_field: r.kv_field.trim(), dest_file: r.dest_file.trim() }]
            : undefined,
      }))
    run('addons', () =>
      updateTemplateAddons(name, addonsFor.flavor, { mcp_servers: mcpSel, engines }).then(() => setAddonsFor(null)),
    )
  }

  if (loading) return <Loading withOverlay description="Loading templates" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>{name} — templates</h2>
        <Button renderIcon={Add} disabled={bases.length === 0} onClick={openCreate}>
          New template
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Create a workspace flavor from a published base template. The image is fixed by the base
        template; the project-static values (Vault paths, repo) are baked in now. Extend a flavor with
        <em> Add-ons</em> to wire MCP servers from the catalog or extra secret engines into the workspace.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}

      {tmpls.length === 0 ? (
        <p>No templates yet. Create one from a base template to make this project launchable.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Flavor</TableHeader>
                <TableHeader>Base version</TableHeader>
                <TableHeader>Image</TableHeader>
                <TableHeader>Node pool</TableHeader>
                <TableHeader>Add-ons</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {tmpls.map((t) => (
                <TableRow key={t.flavor}>
                  <TableCell>{t.flavor}</TableCell>
                  <TableCell>
                    <Tag type="blue">v{t.base_version}</Tag>
                  </TableCell>
                  <TableCell>{t.image}</TableCell>
                  <TableCell>{t.node_pool || 'default'}</TableCell>
                  <TableCell>
                    {(t.addons?.mcp_servers?.length || 0) > 0 && (
                      <Tag type="green">{t.addons?.mcp_servers?.length} MCP</Tag>
                    )}
                    {(t.addons?.engines?.length || 0) > 0 && (
                      <Tag type="purple">{t.addons?.engines?.length} engine</Tag>
                    )}
                    <Button size="sm" kind="ghost" onClick={() => openAddons(t)}>
                      Edit add-ons
                    </Button>
                  </TableCell>
                  <TableCell>
                    <Button
                      size="sm"
                      kind="danger--ghost"
                      disabled={busy === t.flavor}
                      onClick={() => run(t.flavor, () => deleteProjectTemplate(name, t.flavor))}
                    >
                      Delete
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Modal
        open={modalOpen}
        modalHeading="New template from base"
        modalLabel={name}
        primaryButtonText={busy === 'create' ? 'Creating…' : 'Create'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!form.base || !form.git_repo_url.trim() || busy === 'create'}
        onRequestClose={() => setModalOpen(false)}
        onRequestSubmit={submit}
      >
        <Select
          id="base"
          labelText="Base template"
          value={form.base}
          onChange={(e) => {
            const b = bases.find((x) => x.name === e.target.value)
            setForm((f) => ({ ...f, base: e.target.value, node_pool: b?.default_node_pool }))
          }}
        >
          {bases.map((b) => (
            <SelectItem key={b.name} value={b.name} text={`${b.label || b.name} (v${b.version})`} />
          ))}
        </Select>
        <TextInput
          id="flavor"
          labelText="Flavor name (optional; defaults to the base name)"
          style={{ marginTop: '1rem' }}
          value={form.flavor || ''}
          onChange={(e) => setForm((f) => ({ ...f, flavor: e.target.value }))}
        />
        <TextInput
          id="image"
          labelText="Workspace image (fixed by the base template)"
          readOnly
          value={selectedBase?.image || ''}
          style={{ marginTop: '1rem' }}
        />
        <TextInput
          id="git_repo_url"
          labelText="Git repo URL"
          placeholder="https://github.com/org/repo.git"
          style={{ marginTop: '1rem' }}
          value={form.git_repo_url}
          onChange={(e) => setForm((f) => ({ ...f, git_repo_url: e.target.value }))}
        />
        <TextInput
          id="node_pool"
          labelText="Node pool (optional)"
          style={{ marginTop: '1rem' }}
          value={form.node_pool || ''}
          onChange={(e) => setForm((f) => ({ ...f, node_pool: e.target.value }))}
        />
      </Modal>

      <Modal
        open={addonsFor !== null}
        modalHeading={`Add-ons — ${addonsFor?.flavor || ''}`}
        modalLabel={name}
        primaryButtonText={busy === 'addons' ? 'Applying…' : 'Apply add-ons'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={busy === 'addons'}
        onRequestClose={() => setAddonsFor(null)}
        onRequestSubmit={saveAddons}
      >
        <h4 style={{ marginBottom: '0.5rem' }}>MCP servers</h4>
        <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '0.5rem' }}>
          Wire deployed MCP servers into the workspace (registered with Claude Code at launch).
        </p>
        {deployedMcp.length === 0 ? (
          <p style={{ color: 'var(--cds-text-secondary)' }}>
            No MCP servers deployed yet — deploy one from the “mcp servers” page first.
          </p>
        ) : (
          deployedMcp.map((m) => (
            <Checkbox
              key={m.name}
              id={`mcp-${m.name}`}
              labelText={m.name}
              checked={mcpSel.includes(m.name)}
              onChange={(_: unknown, { checked }: { checked: boolean }) =>
                setMcpSel((s) => (checked ? [...s, m.name] : s.filter((x) => x !== m.name)))
              }
            />
          ))
        )}

        <h4 style={{ margin: '1.5rem 0 0.5rem' }}>Extra secret engines</h4>
        <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '0.5rem' }}>
          Mount an extra Vault engine in the project namespace and surface one secret as a
          <code> /secrets</code> file the workspace reads over WIF.
        </p>
        {engineRows.map((r, i) => (
          <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '0.5rem' }}>
            <TextInput
              id={`eng-mount-${i}`}
              labelText="Mount"
              value={r.mount}
              onChange={(e) => setEngineRows((rows) => rows.map((x, j) => (j === i ? { ...x, mount: e.target.value } : x)))}
            />
            <TextInput
              id={`eng-type-${i}`}
              labelText="Type"
              value={r.type}
              onChange={(e) => setEngineRows((rows) => rows.map((x, j) => (j === i ? { ...x, type: e.target.value } : x)))}
            />
            <TextInput
              id={`eng-kvpath-${i}`}
              labelText="KV path"
              value={r.kv_path}
              onChange={(e) => setEngineRows((rows) => rows.map((x, j) => (j === i ? { ...x, kv_path: e.target.value } : x)))}
            />
            <TextInput
              id={`eng-field-${i}`}
              labelText="KV field"
              value={r.kv_field}
              onChange={(e) => setEngineRows((rows) => rows.map((x, j) => (j === i ? { ...x, kv_field: e.target.value } : x)))}
            />
            <TextInput
              id={`eng-dest-${i}`}
              labelText="/secrets file"
              value={r.dest_file}
              onChange={(e) => setEngineRows((rows) => rows.map((x, j) => (j === i ? { ...x, dest_file: e.target.value } : x)))}
            />
            <Button
              hasIconOnly
              size="md"
              kind="danger--ghost"
              iconDescription="Remove"
              renderIcon={TrashCan}
              onClick={() => setEngineRows((rows) => rows.filter((_, j) => j !== i))}
            />
          </div>
        ))}
        <Button
          size="sm"
          kind="ghost"
          renderIcon={Add}
          onClick={() => setEngineRows((rows) => [...rows, { mount: '', type: 'kv-v2', kv_path: '', kv_field: '', dest_file: '' }])}
        >
          Add engine
        </Button>
      </Modal>
    </div>
  )
}
