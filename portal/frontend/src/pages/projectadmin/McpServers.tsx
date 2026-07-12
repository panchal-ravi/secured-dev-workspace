import { useEffect, useState, type CSSProperties } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button, InlineLoading, InlineNotification, Loading, Modal, NumberInput, Select, SelectItem, Stack,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow,
  Tag, TextArea, TextInput,
} from '@carbon/react'
import { Add } from '@carbon/icons-react'
import {
  listProjectMcp, deployProjectMcp, testProjectMcp, deleteProjectMcp, updateProjectMcpServer,
  CredentialSpec, DeployMcpServerInput, ParamSpec, PathGrant, ProjectMcpDeployed,
} from '../../api/client'

const PRE_STYLE: CSSProperties = {
  background: 'var(--cds-layer)',
  border: '1px solid var(--cds-border-subtle)',
  padding: '0.75rem',
  fontSize: '0.75rem',
  overflowX: 'auto',
  whiteSpace: 'pre',
  margin: 0,
}

// Read-only labeled value / multiline block for the configuration view. Both
// render nothing when the field was not set, so the modal only shows what the
// deploy actually configured.
function DetailRow({ label, value }: { label: string; value?: string }) {
  if (!value) return null
  return (
    <div>
      <div className="cds--label">{label}</div>
      <div style={{ fontSize: '0.875rem' }}>{value}</div>
    </div>
  )
}

function DetailBlock({ label, text }: { label: string; text?: string }) {
  if (!text) return null
  return (
    <div>
      <div className="cds--label">{label}</div>
      <pre style={PRE_STYLE}>{text}</pre>
    </div>
  )
}

function kvLines(m?: Record<string, string>): string {
  return Object.entries(m || {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

function grantLines(grants?: PathGrant[]): string {
  return (grants || []).map((g) => `${g.path} ${g.capabilities.join(',')}`).join('\n')
}

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = { deployed: 'blue', published: 'green' }

// The dynamic source (Vault-mounted DB/AWS engines) is supported by the backend but
// deliberately not offered in the UI yet.
const SOURCES: CredentialSpec['source'][] = ['none', 'wif-token', 'static']

// Prefill for the static source: one example secret key plus the env template that
// reads it back from Vault at job runtime. Every ${token} in the secret data becomes
// a masked parameter input; values go write-only to Vault, never into the stored spec.
const STATIC_PREFILL = {
  staticText: 'api_key=${api_key}',
  envTplText: 'API_KEY={{ with secret "${cred_path}" }}{{ .Data.data.api_key }}{{ end }}',
}

const STATIC_EXAMPLE = [
  '# Secret data — each ${token} becomes a masked input below:',
  'client_id=${client_id}',
  'client_secret=${client_secret}',
  '',
  '# Matching env templates (read back from Vault at job runtime):',
  'CLIENT_ID={{ with secret "${cred_path}" }}{{ .Data.data.client_id }}{{ end }}',
  'CLIENT_SECRET={{ with secret "${cred_path}" }}{{ .Data.data.client_secret }}{{ end }}',
].join('\n')

// The policy every wif-token deploy gets (mirrors backend DeriveCredentialPolicy
// with the platform's KV mount): read over the project KV's projects/ subtree,
// inside the project's own Vault namespace, plus read on sys/mounts (namespace-
// relative; vault-mcp-server needs it for KV version detection).
const WIF_DEFAULT_POLICY = [
  'path "secret/data/projects/*" {',
  '  capabilities = ["read"]',
  '}',
  'path "sys/mounts" {',
  '  capabilities = ["read"]',
  '}',
].join('\n')

const GRANTS_HELP = [
  '# One grant per line: <path> <capabilities, comma-separated>',
  'secret/metadata/projects/* list,read',
  'secret/data/projects/team-config read',
  '',
  '# Rules (enforced server-side):',
  '# - paths resolve inside the project\'s Vault namespace only',
  '# - capabilities: read, list, create, update, delete',
  '# - denied: sys/, auth/, identity/, cubbyhole/ prefixes and ".." segments',
  '#   ("sys/mounts read" is already in the derived baseline above — no grant',
  '#   needed; subpaths like sys/mounts/<mount> stay denied.)',
].join('\n')

// parseGrants turns "path caps" lines into PathGrants: path up to the first
// whitespace, then comma-separated capabilities.
function parseGrants(text: string): PathGrant[] {
  const out: PathGrant[] = []
  for (const line of parseLines(text)) {
    const m = line.match(/^(\S+)\s+(\S+)$/)
    if (!m) throw new Error(`additional paths: line "${line}" is not "<path> <capabilities>"`)
    out.push({ path: m[1], capabilities: m[2].split(',').map((c) => c.trim()).filter(Boolean) })
  }
  return out
}

// parseKV turns "KEY=value" lines into a map, splitting on the first "=" only
// (values routinely contain more of them). Non-blank lines without "=" error.
function parseKV(text: string, what: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    if (!line.trim()) continue
    const i = line.indexOf('=')
    if (i <= 0) throw new Error(`${what}: line "${line.trim()}" is not KEY=value`)
    out[line.slice(0, i).trim()] = line.slice(i + 1)
  }
  return out
}

function parseLines(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean)
}

// paramTokens extracts the unique ${token} names from the static secret data.
function paramTokens(text: string): string[] {
  return Array.from(new Set(Array.from(text.matchAll(/\$\{([a-zA-Z0-9_]+)\}/g), (m) => m[1])))
}

interface DeployForm {
  name: string
  image: string
  transport: string
  port: number
  path: string
  commandText: string
  envText: string
  source: CredentialSpec['source']
  staticText: string
  envTplText: string
  grantsText: string
  paramValues: Record<string, string>
}

function emptyForm(): DeployForm {
  return {
    name: '',
    image: '',
    transport: 'sse',
    port: 8080,
    path: '',
    commandText: '',
    envText: '',
    source: 'none',
    staticText: '',
    envTplText: '',
    grantsText: '',
    paramValues: {},
  }
}

// EditForm is the mutable container definition of a deployed server — everything
// but the credential source, which requires delete + redeploy.
interface EditForm {
  image: string
  transport: string
  port: number
  path: string
  commandText: string
  envText: string
}

function emptyEditForm(): EditForm {
  return { image: '', transport: 'sse', port: 8080, path: '', commandText: '', envText: '' }
}

export default function McpServers() {
  const { name = '' } = useParams()
  const [deployed, setDeployed] = useState<ProjectMcpDeployed[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [modalErr, setModalErr] = useState('')
  const [view, setView] = useState<ProjectMcpDeployed | null>(null)
  const [showExample, setShowExample] = useState(false)
  const [showGrantsHelp, setShowGrantsHelp] = useState(false)
  const [form, setForm] = useState<DeployForm>(emptyForm)
  const [edit, setEdit] = useState<ProjectMcpDeployed | null>(null)
  const [editForm, setEditForm] = useState<EditForm>(emptyEditForm)
  const [policyText, setPolicyText] = useState('')
  const [editErr, setEditErr] = useState('')

  const refresh = () =>
    listProjectMcp(name)
      .then((d) => setDeployed(d.deployed || []))
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  // Fresh form on every open — stale state from a previous authoring session
  // must never leak into a new deploy.
  useEffect(() => {
    if (modalOpen) {
      setForm(emptyForm())
      setModalErr('')
      setShowExample(false)
      setShowGrantsHelp(false)
    }
  }, [modalOpen])

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

  const set = (patch: Partial<DeployForm>) => setForm((f) => ({ ...f, ...patch }))

  // Switching source prefills ONLY the credential-section fields (server fields are
  // never touched); the static example gives a working starting point.
  const pickSource = (source: CredentialSpec['source']) =>
    setForm((f) => ({
      ...f,
      source,
      staticText: source === 'static' ? STATIC_PREFILL.staticText : '',
      envTplText: source === 'static' ? STATIC_PREFILL.envTplText : '',
      grantsText: '',
      paramValues: {},
    }))

  const tokens = form.source === 'static' ? paramTokens(form.staticText) : []

  const assemble = (): DeployMcpServerInput => {
    const credential: CredentialSpec = { source: form.source }
    if (form.source === 'static') {
      credential.static = { data: parseKV(form.staticText, 'static data') }
      // Declare every ${token} as a secret param so the backend strips its value
      // from the persisted row (only the placeholder spec is stored).
      credential.params = tokens.map((t): ParamSpec => ({ name: t, type: 'secret', required: true }))
    }
    const envTpl = parseKV(form.envTplText, 'env templates')
    if (Object.keys(envTpl).length > 0) credential.env_templates = envTpl

    const input: DeployMcpServerInput = {
      name: form.name.trim(),
      image: form.image.trim(),
      transport: form.transport,
      port: form.port,
      credential,
    }
    if (form.path.trim()) input.path = form.path.trim()
    const command = parseLines(form.commandText)
    if (command.length > 0) input.command = command
    const env = parseKV(form.envText, 'env vars')
    if (Object.keys(env).length > 0) input.env = env
    const params: Record<string, string> = {}
    for (const t of tokens) {
      if (!form.paramValues[t]) throw new Error(`missing value for parameter ${t}`)
      params[t] = form.paramValues[t]
    }
    if (Object.keys(params).length > 0) input.params = params
    if (form.source === 'wif-token') {
      const grants = parseGrants(form.grantsText)
      if (grants.length > 0) input.extra_grants = grants
    }
    return input
  }

  const openEdit = (s: ProjectMcpDeployed) => {
    setEditForm({
      image: s.image || '',
      transport: s.transport || 'sse',
      port: s.port || 8080,
      path: s.path || '',
      commandText: (s.command || []).join('\n'),
      envText: kvLines(s.env),
    })
    setPolicyText(grantLines(s.instance?.extra_grants))
    setEditErr('')
    setShowGrantsHelp(false)
    setEdit(s)
  }

  const setE = (patch: Partial<EditForm>) => setEditForm((f) => ({ ...f, ...patch }))

  const submitEdit = async () => {
    if (!edit) return
    setBusy('edit')
    setEditErr('')
    try {
      // Grants are UI-editable for wif-token only; other sources pass their
      // persisted grants through unchanged (the backend replaces what's sent).
      const grants =
        edit.credential?.source === 'wif-token' ? parseGrants(policyText) : edit.instance?.extra_grants || []
      const command = parseLines(editForm.commandText)
      await updateProjectMcpServer(name, edit.name, {
        image: editForm.image.trim(),
        ...(command.length > 0 ? { command } : {}),
        transport: editForm.transport,
        port: editForm.port,
        ...(editForm.path.trim() ? { path: editForm.path.trim() } : {}),
        env: parseKV(editForm.envText, 'env vars'),
        extra_grants: grants,
      })
      setEdit(null)
      await refresh()
    } catch (e) {
      setEditErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  const submitDeploy = async () => {
    setBusy('deploy')
    setModalErr('')
    try {
      const input = assemble()
      await deployProjectMcp(name, input)
      setModalOpen(false)
      await refresh()
    } catch (e) {
      setModalErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading MCP servers" />

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>{name} — MCP servers</h2>
        <Button renderIcon={Add} onClick={() => setModalOpen(true)}>
          Deploy MCP server
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Author and deploy an MCP server into this project. Its credential is provisioned into the
        project&apos;s Vault namespace at deploy — never pasted into the job.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}

      {deployed.length === 0 ? (
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
              {deployed.map((s) => {
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
                      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
                        <Button size="sm" kind="ghost" disabled={busy !== ''} onClick={() => setView(s)}>
                          View
                        </Button>
                        <Button size="sm" kind="ghost" disabled={busy !== ''} onClick={() => openEdit(s)}>
                          Edit
                        </Button>
                        {busy === `test:${s.name}` ? (
                          <InlineLoading description="Testing…" />
                        ) : (
                          <Button size="sm" kind="tertiary" disabled={busy !== ''} onClick={() => run(`test:${s.name}`, () => testProjectMcp(name, s.name))}>
                            Test
                          </Button>
                        )}
                        {busy === `del:${s.name}` ? (
                          <InlineLoading description="Deleting…" />
                        ) : (
                          <Button size="sm" kind="danger--ghost" disabled={busy !== ''} onClick={() => run(`del:${s.name}`, () => deleteProjectMcp(name, s.name))}>
                            Delete
                          </Button>
                        )}
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
        open={view !== null}
        passiveModal
        size="lg"
        modalHeading={view ? `${view.name} — configuration` : ''}
        modalLabel={name}
        onRequestClose={() => setView(null)}
      >
        {view && (
          <Stack gap={5}>
            <h4>Server</h4>
            <DetailRow label="Container image" value={view.image} />
            <DetailRow label="Transport" value={view.transport} />
            <DetailRow label="Container port" value={view.port ? String(view.port) : undefined} />
            <DetailRow label="Path" value={view.path || '(default for transport)'} />
            <DetailBlock label="Command args" text={(view.command || []).join('\n')} />
            <DetailBlock label="Env vars" text={kvLines(view.env)} />

            <h4>Credential</h4>
            <DetailRow label="Source" value={view.credential?.source || 'none'} />
            {view.credential?.source === 'static' && (
              <DetailBlock
                label="Secret data (values are write-only in Vault — placeholders shown)"
                text={kvLines(view.credential.static?.data)}
              />
            )}
            {view.credential?.source === 'wif-token' && (
              <>
                <DetailBlock label="Vault policy (derived — always granted)" text={WIF_DEFAULT_POLICY} />
                <DetailBlock label="Additional Vault paths" text={grantLines(view.instance?.extra_grants)} />
              </>
            )}
            {view.credential?.source === 'dynamic' && (
              <DetailBlock label="Dynamic engine spec" text={JSON.stringify(view.credential.dynamic, null, 2)} />
            )}
            <DetailBlock label="Env templates" text={kvLines(view.credential?.env_templates)} />
            <DetailBlock label="Non-secret parameters" text={kvLines(view.params)} />

            <h4>Wiring</h4>
            <DetailRow label="Gateway peer URL" value={view.gateway_url} />
            <DetailRow label="Nomad job" value={view.job_id} />
            <DetailRow label="Deployed by" value={view.created_by} />
            <DetailRow label="Deployed at" value={new Date(view.created_at).toLocaleString()} />
          </Stack>
        )}
      </Modal>

      <Modal
        open={edit !== null}
        modalHeading={edit ? `${edit.name} — edit` : ''}
        modalLabel={name}
        size="lg"
        primaryButtonText={busy === 'edit' ? 'Saving…' : 'Save'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!editForm.image.trim() || busy === 'edit'}
        onRequestClose={() => setEdit(null)}
        onRequestSubmit={submitEdit}
      >
        <Stack gap={5}>
          {editErr && (
            <InlineNotification
              kind="error"
              title="Update failed"
              subtitle={editErr}
              lowContrast
              onCloseButtonClick={() => setEditErr('')}
            />
          )}
          <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.875rem' }}>
            Vault-path changes apply immediately (policies are evaluated per request) — no restart.
            A server change (image, transport, port, path, command, env) restarts the server on a
            new address and updates the gateway route in place: existing workspaces keep working.
            (Rare fallback: if the route update fails, the wiring is rebuilt with a fresh token — a
            workspace whose calls to this server start failing afterwards must be recreated.) Only
            the credential source still requires delete + redeploy.
          </p>
          <h4>Server</h4>
          <TextInput
            id="edit-image"
            labelText="Container image"
            value={editForm.image}
            onChange={(e) => setE({ image: e.target.value })}
          />
          <Select
            id="edit-transport"
            labelText="Transport"
            value={editForm.transport}
            onChange={(e) => setE({ transport: e.target.value })}
          >
            <SelectItem value="sse" text="sse" />
            <SelectItem value="streamable-http" text="streamable-http" />
          </Select>
          <NumberInput
            id="edit-port"
            label="Container port"
            helperText="Host-side port is allocated dynamically; must match the port the server listens on (e.g. TRANSPORT_PORT)"
            min={1}
            max={65535}
            value={editForm.port}
            onChange={(_, { value }) => setE({ port: Number(value) || 0 })}
          />
          <TextInput
            id="edit-path"
            labelText="Path (optional)"
            helperText="Defaults to /sse or /mcp by transport"
            value={editForm.path}
            onChange={(e) => setE({ path: e.target.value })}
          />
          <TextArea
            id="edit-command"
            labelText="Command args (one per line, optional)"
            rows={3}
            value={editForm.commandText}
            onChange={(e) => setE({ commandText: e.target.value })}
          />
          <TextArea
            id="edit-env"
            labelText="Env vars (KEY=value, one per line, optional; non-secret only)"
            rows={4}
            value={editForm.envText}
            onChange={(e) => setE({ envText: e.target.value })}
          />
          {edit?.credential?.source === 'wif-token' && (
            <>
              <h4>Credential</h4>
              <DetailBlock label="Vault policy (derived — always granted)" text={WIF_DEFAULT_POLICY} />
              <TextArea
                id="policy-grants"
                labelText="Additional Vault paths (one per line; empty = derived policy only)"
                helperText="<path> <capabilities, comma-separated> — confined to this project's Vault namespace"
                placeholder="secret/metadata/projects/* list,read"
                rows={3}
                value={policyText}
                onChange={(e) => setPolicyText(e.target.value)}
              />
              <Button kind="ghost" size="sm" onClick={() => setShowGrantsHelp((v) => !v)}>
                {showGrantsHelp ? 'Hide help: additional paths' : 'Show help: additional paths'}
              </Button>
              {showGrantsHelp && <pre style={PRE_STYLE}>{GRANTS_HELP}</pre>}
            </>
          )}
        </Stack>
      </Modal>

      <Modal
        open={modalOpen}
        size="lg"
        modalHeading="Deploy MCP server"
        modalLabel={name}
        primaryButtonText={busy === 'deploy' ? 'Deploying…' : 'Deploy'}
        secondaryButtonText="Cancel"
        primaryButtonDisabled={!form.name.trim() || !form.image.trim() || busy === 'deploy'}
        onRequestClose={() => setModalOpen(false)}
        onRequestSubmit={submitDeploy}
      >
        <Stack gap={5}>
          {modalErr && (
            <InlineNotification
              kind="error"
              title="Deploy failed"
              subtitle={modalErr}
              lowContrast
              onCloseButtonClick={() => setModalErr('')}
            />
          )}
          <h4>Server</h4>
          <TextInput
            id="mcp-name"
            labelText="Name"
            helperText="3-40 chars, lowercase letters/digits/dashes"
            placeholder="postgres-mcp"
            value={form.name}
            onChange={(e) => set({ name: e.target.value })}
          />
          <TextInput
            id="mcp-image"
            labelText="Container image"
            placeholder="org/mcp-server:tag"
            value={form.image}
            onChange={(e) => set({ image: e.target.value })}
          />
          <Select
            id="mcp-transport"
            labelText="Transport"
            value={form.transport}
            onChange={(e) => set({ transport: e.target.value })}
          >
            <SelectItem value="sse" text="sse" />
            <SelectItem value="streamable-http" text="streamable-http" />
          </Select>
          <NumberInput
            id="mcp-port"
            label="Container port"
            helperText="Host-side port is allocated dynamically"
            min={1}
            max={65535}
            value={form.port}
            onChange={(_, { value }) => set({ port: Number(value) || 0 })}
          />
          <TextInput
            id="mcp-path"
            labelText="Path (optional)"
            helperText="Defaults to /sse or /mcp by transport"
            value={form.path}
            onChange={(e) => set({ path: e.target.value })}
          />
          <TextArea
            id="mcp-command"
            labelText="Command args (one per line, optional)"
            rows={3}
            value={form.commandText}
            onChange={(e) => set({ commandText: e.target.value })}
          />
          <TextArea
            id="mcp-env"
            labelText="Env vars (KEY=value, one per line, optional; non-secret only)"
            rows={2}
            value={form.envText}
            onChange={(e) => set({ envText: e.target.value })}
          />

          <h4>Credential</h4>
          <Select
            id="cred-source"
            labelText="Source"
            helperText="none = no injected credential; wif-token = scoped short-lived VAULT_TOKEN; static = pasted secrets stored write-only in project Vault KV"
            value={form.source}
            onChange={(e) => pickSource(e.target.value as CredentialSpec['source'])}
          >
            {SOURCES.map((s) => (
              <SelectItem key={s} value={s} text={s} />
            ))}
          </Select>
          {form.source === 'wif-token' && (
            <>
              <div>
                <div className="cds--label">Vault policy (derived — always granted)</div>
                <pre
                  style={{
                    background: 'var(--cds-layer)',
                    border: '1px solid var(--cds-border-subtle)',
                    padding: '0.75rem',
                    fontSize: '0.75rem',
                    overflowX: 'auto',
                    whiteSpace: 'pre',
                    margin: 0,
                  }}
                >
                  {WIF_DEFAULT_POLICY}
                </pre>
              </div>
              <TextArea
                id="wif-grants"
                labelText="Additional Vault paths (one per line, optional)"
                helperText="<path> <capabilities, comma-separated> — appended to the derived policy above; confined to this project's Vault namespace"
                placeholder="secret/metadata/projects/* list,read"
                rows={2}
                value={form.grantsText}
                onChange={(e) => set({ grantsText: e.target.value })}
              />
              <Button kind="ghost" size="sm" onClick={() => setShowGrantsHelp((v) => !v)}>
                {showGrantsHelp ? 'Hide help: additional paths' : 'Show help: additional paths'}
              </Button>
              {showGrantsHelp && (
                <pre
                  style={{
                    background: 'var(--cds-layer)',
                    border: '1px solid var(--cds-border-subtle)',
                    padding: '0.75rem',
                    fontSize: '0.75rem',
                    overflowX: 'auto',
                    whiteSpace: 'pre',
                  }}
                >
                  {GRANTS_HELP}
                </pre>
              )}
            </>
          )}
          {form.source === 'static' && (
            <>
              <TextArea
                id="static-data"
                labelText="Secret data (KEY=${token}, one per line; each ${token} becomes a masked input below)"
                rows={2}
                value={form.staticText}
                onChange={(e) => set({ staticText: e.target.value })}
              />
              <Button kind="ghost" size="sm" onClick={() => setShowExample((v) => !v)}>
                {showExample ? 'Hide example: multiple secrets' : 'Show example: multiple secrets'}
              </Button>
              {showExample && (
                <pre
                  style={{
                    background: 'var(--cds-layer)',
                    border: '1px solid var(--cds-border-subtle)',
                    padding: '0.75rem',
                    fontSize: '0.75rem',
                    overflowX: 'auto',
                    whiteSpace: 'pre',
                  }}
                >
                  {STATIC_EXAMPLE}
                </pre>
              )}
            </>
          )}
          <TextArea
            id="env-templates"
            labelText="Env templates (KEY=value, one per line, optional)"
            helperText={
              'Values are consul-template snippets rendered at job runtime; ${...} tokens (cred_path + params) are filled at deploy. A malformed {{ }} template surfaces as a failed task at deploy.'
            }
            rows={3}
            value={form.envTplText}
            onChange={(e) => set({ envTplText: e.target.value })}
          />

          {tokens.length > 0 && (
            <div>
              <h4>Parameters</h4>
              <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.75rem', marginTop: '0.25rem' }}>
                {'One masked input per ${token} in the secret data. To add a parameter, add another KEY=${token} line to "Secret data" above (see the example).'}
              </p>
            </div>
          )}
          {tokens.map((t) => (
            <TextInput
              key={t}
              id={`param-${t}`}
              labelText={`${t} *`}
              helperText="stored write-only in Vault KV"
              type="password"
              value={form.paramValues[t] || ''}
              onChange={(e) =>
                setForm((f) => ({ ...f, paramValues: { ...f.paramValues, [t]: e.target.value } }))
              }
            />
          ))}
        </Stack>
      </Modal>
    </div>
  )
}
