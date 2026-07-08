import { useEffect, useState, type CSSProperties } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button, Checkbox, InlineLoading, InlineNotification, Loading, Modal, Select, SelectItem,
  Table, TableBody, TableCell, TableContainer, TableHead, TableHeader, TableRow, Tag, TextArea,
} from '@carbon/react'
import { Add, Chat } from '@carbon/icons-react'
import {
  listAgentTemplates, saveAgentTemplate, deployAgentTemplate, testAgentTemplate,
  publishAgentTemplate, deleteAgentTemplate, validateAgentTemplate, chatWithTemplate,
  listProjectMcp, listMcpServerTools,
  McpTool, ProjectAgentTemplateView,
} from '../../api/client'
import AgentChatPanel from '../../components/AgentChatPanel'

const YAML_STYLE: CSSProperties = { fontFamily: 'var(--cds-code-01-font-family, monospace)', minHeight: '22rem' }

// STARTER pre-fills the create modal — the strict v1 schema. Each mcp_servers
// item is either a bare server name (all tools) or a {server, tools} map (subset).
// Use the picker below to append entries here automatically.
const STARTER = `spec_version: v1
kind: native
name: my-agent
description: What this agent does.
instructions: |
  You are a helpful assistant. Describe the agent's behaviour here.
llm: deepseek-v4-pro
greeting: "Hi! How can I help?"
tools:
  # mcp_servers items — a bare server name (all tools) or a {server, tools} map (subset):
  #   - github
  #   - {server: github, tools: [get_issue]}
  mcp_servers: []
  builtins: [planning, filesystem]
`

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = { draft: 'gray', tested: 'blue', published: 'green' }

// insertMcpEntry appends a list item (e.g. "- {server: github, tools: [get_issue]}")
// under the YAML's tools.mcp_servers: key, converting an inline "[]"/"[a, b]" list to
// block form as needed. Returns null if there is no mcp_servers key to insert into.
function insertMcpEntry(yaml: string, entry: string): string | null {
  const lines = yaml.split('\n')
  const idx = lines.findIndex((l) => /^\s*mcp_servers\s*:/.test(l))
  if (idx === -1) return null

  const line = lines[idx]
  const keyIndent = /^(\s*)/.exec(line)?.[1] ?? ''
  const itemIndent = keyIndent + '  '
  const item = `${itemIndent}${entry}`

  // Inline empty list: `mcp_servers: []` → block form with the new item.
  if (/^\s*mcp_servers\s*:\s*\[\s*\]\s*$/.test(line)) {
    lines[idx] = `${keyIndent}mcp_servers:`
    lines.splice(idx + 1, 0, item)
    return lines.join('\n')
  }
  // Inline non-empty list: `mcp_servers: [a, b]` → block form preserving existing items.
  const inline = /^\s*mcp_servers\s*:\s*\[(.+)\]\s*$/.exec(line)
  if (inline) {
    const existing = inline[1].split(',').map((s) => s.trim()).filter(Boolean)
    lines[idx] = `${keyIndent}mcp_servers:`
    lines.splice(idx + 1, 0, ...existing.map((e) => `${itemIndent}- ${e}`), item)
    return lines.join('\n')
  }
  // Already block form: insert after the last child line of the key.
  let insertAt = idx + 1
  for (let i = idx + 1; i < lines.length; i++) {
    const ind = (/^(\s*)/.exec(lines[i])?.[1] ?? '').length
    if (lines[i].trim() !== '' && ind > keyIndent.length) insertAt = i + 1
    else break
  }
  lines.splice(insertAt, 0, item)
  return lines.join('\n')
}

// ToolBrowser lets an admin pick a deployed server, check a subset of its tools,
// and insert the matching {server, tools:[...]} entry straight into the YAML.
function ToolBrowser({ project, onInsert }: { project: string; onInsert: (entry: string) => void }) {
  const [servers, setServers] = useState<string[]>([])
  const [server, setServer] = useState('')
  const [tools, setTools] = useState<McpTool[]>([])
  const [checked, setChecked] = useState<Record<string, boolean>>({})
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    listProjectMcp(project)
      .then((d) => setServers((d.deployed || []).map((s) => s.name)))
      .catch(() => setServers([]))
  }, [project])

  const pick = (name: string) => {
    setServer(name)
    setChecked({})
    setTools([])
    if (!name) return
    setLoading(true)
    listMcpServerTools(project, name)
      .then((d) => setTools(d.tools || []))
      .catch(() => setTools([]))
      .finally(() => setLoading(false))
  }

  const picked = tools.filter((t) => checked[t.name]).map((t) => t.name)
  const snippet =
    picked.length > 0
      ? `- {server: ${server}, tools: [${picked.join(', ')}]}`
      : server
        ? `- ${server}`
        : ''

  const insert = () => {
    if (!snippet) return
    onInsert(snippet)
    setChecked({})
  }

  return (
    <div style={{ marginTop: '1rem', borderTop: '1px solid var(--cds-border-subtle)', paddingTop: '0.75rem' }}>
      <div className="cds--label">Add MCP tools — pick a server, then insert it (all tools) or a checked subset</div>
      <Select id="tool-browser-server" labelText="" value={server} onChange={(e) => pick(e.target.value)} size="sm">
        <SelectItem value="" text="Select a deployed MCP server…" />
        {servers.map((s) => (
          <SelectItem key={s} value={s} text={s} />
        ))}
      </Select>
      {loading && <InlineLoading description="Loading tools" />}
      {!loading && server && tools.length === 0 && (
        <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem' }}>
          No tools cached — run Test on this server (MCP servers page) first.
        </p>
      )}
      <div
        style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(auto-fill, minmax(20rem, 1fr))',
          columnGap: '1rem',
          rowGap: '0.25rem',
          marginTop: '0.5rem',
        }}
      >
        {tools.map((t) => (
          <Checkbox
            key={t.id}
            id={`tool-${t.id}`}
            labelText={t.name}
            title={t.description}
            checked={!!checked[t.name]}
            onChange={(_e, { checked: c }) => setChecked((m) => ({ ...m, [t.name]: c }))}
          />
        ))}
      </div>
      {snippet && (
        <div style={{ marginTop: '0.5rem', display: 'flex', alignItems: 'center', gap: '0.75rem', flexWrap: 'wrap' }}>
          <Button kind="tertiary" size="sm" renderIcon={Add} onClick={insert}>
            Insert into YAML
          </Button>
          <code style={{ fontSize: '0.8rem', color: 'var(--cds-text-secondary)' }}>{snippet}</code>
        </div>
      )}
    </div>
  )
}

export default function AgentTemplates() {
  const { name = '' } = useParams()
  const [templates, setTemplates] = useState<ProjectAgentTemplateView[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  const [editorOpen, setEditorOpen] = useState(false)
  const [editing, setEditing] = useState<string | null>(null)
  const [yaml, setYaml] = useState('')
  const [validateMsg, setValidateMsg] = useState('')
  const [validateKind, setValidateKind] = useState<'success' | 'error'>('success')

  const [chatTmpl, setChatTmpl] = useState<ProjectAgentTemplateView | null>(null)

  const refresh = () =>
    listAgentTemplates(name)
      .then((d) => setTemplates(d.templates || []))
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  // Poll while a test job is still coming up.
  useEffect(() => {
    if (!templates.some((t) => t.job_id && !t.running)) return
    const id = setInterval(refresh, 2000)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [templates])

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
    setEditing(null)
    setYaml(STARTER)
    setValidateMsg('')
    setEditorOpen(true)
  }

  const openEdit = (t: ProjectAgentTemplateView) => {
    setEditing(t.name)
    setYaml(t.yaml_source)
    setValidateMsg('')
    setEditorOpen(true)
  }

  const validate = async () => {
    setValidateMsg('')
    try {
      await validateAgentTemplate(name, yaml)
      setValidateKind('success')
      setValidateMsg('Valid — save it, then deploy-test.')
    } catch (e) {
      setValidateKind('error')
      setValidateMsg((e as Error).message)
    }
  }

  const insertTool = (entry: string) => setYaml((y) => insertMcpEntry(y, entry) ?? y)

  const save = async () => {
    setBusy('save')
    setErr('')
    try {
      await saveAgentTemplate(name, yaml, editing || undefined)
      setEditorOpen(false)
      await refresh()
    } catch (e) {
      setValidateKind('error')
      setValidateMsg((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading agent templates" />

  const toolCount = (t: ProjectAgentTemplateView) => Object.keys(t.tool_selection || {}).length

  return (
    <div className="page">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <h2>{name} — agent templates</h2>
        <Button renderIcon={Add} onClick={openCreate} disabled={busy !== ''}>
          New template
        </Button>
      </div>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0 0 1rem' }}>
        Author an agent template, deploy-test it, then publish it as a card. Project users spin up
        their own isolated instance of a published card.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}

      {templates.length === 0 ? (
        <p>No templates yet. Create one to get started.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Model</TableHeader>
                <TableHeader>Servers</TableHeader>
                <TableHeader>Version</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Test</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {templates.map((t) => (
                <TableRow key={t.name}>
                  <TableCell>{t.name}</TableCell>
                  <TableCell>{t.model}</TableCell>
                  <TableCell>{toolCount(t) || '—'}</TableCell>
                  <TableCell>{t.version}</TableCell>
                  <TableCell>
                    <div style={{ display: 'flex', gap: '0.25rem' }}>
                      <Tag type={statusTag[t.status] || 'gray'} size="sm">
                        {t.status}
                      </Tag>
                      {t.running && (
                        <Tag type="green" size="sm">
                          running
                        </Tag>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    {t.test_result ? (
                      <Tag type={t.test_result.passed ? 'green' : 'red'} size="sm">
                        {t.test_result.passed ? `${t.test_result.tools_discovered} tools` : 'failed'}
                      </Tag>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                  <TableCell>
                    <div style={{ display: 'flex', gap: '0.25rem', flexWrap: 'wrap', alignItems: 'center' }}>
                      {busy === `deploy:${t.name}` ? (
                        <InlineLoading description="Deploying…" />
                      ) : busy === `test:${t.name}` ? (
                        <InlineLoading description="Testing…" />
                      ) : busy === `pub:${t.name}` ? (
                        <InlineLoading description="Publishing…" />
                      ) : busy === `del:${t.name}` ? (
                        <InlineLoading description="Deleting…" />
                      ) : (
                        <>
                          <Button kind="ghost" size="sm" disabled={busy !== ''} onClick={() => run(`deploy:${t.name}`, () => deployAgentTemplate(name, t.name))}>
                            Deploy test
                          </Button>
                          <Button kind="ghost" size="sm" disabled={busy !== '' || !t.job_id} onClick={() => run(`test:${t.name}`, () => testAgentTemplate(name, t.name))}>
                            Test
                          </Button>
                          <Button kind="ghost" size="sm" renderIcon={Chat} disabled={busy !== '' || !t.running} onClick={() => setChatTmpl(t)}>
                            Chat
                          </Button>
                          <Button kind="ghost" size="sm" disabled={busy !== '' || t.status !== 'tested'} onClick={() => run(`pub:${t.name}`, () => publishAgentTemplate(name, t.name))}>
                            Publish
                          </Button>
                          <Button kind="ghost" size="sm" disabled={busy !== ''} onClick={() => openEdit(t)}>
                            Edit
                          </Button>
                          <Button kind="danger--ghost" size="sm" disabled={busy !== ''} onClick={() => run(`del:${t.name}`, () => deleteAgentTemplate(name, t.name))}>
                            Delete
                          </Button>
                        </>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <Modal
        open={editorOpen}
        modalHeading={editing ? `Edit template — ${editing}` : 'New template'}
        primaryButtonText={busy === 'save' ? 'Saving…' : 'Save draft'}
        secondaryButtonText="Validate"
        primaryButtonDisabled={busy !== ''}
        onRequestClose={() => setEditorOpen(false)}
        onRequestSubmit={save}
        onSecondarySubmit={validate}
        size="lg"
      >
        <p style={{ marginBottom: '1rem', color: 'var(--cds-text-secondary)' }}>
          The template&apos;s <code>name</code> keys it; saving stores a draft. Deploy-test and
          publish are separate steps on the row.
        </p>
        <TextArea id="template-yaml" labelText="Template YAML" rows={20} style={YAML_STYLE} value={yaml} onChange={(e) => setYaml(e.target.value)} />
        {validateMsg && (
          <div style={{ marginTop: '0.75rem' }}>
            <InlineNotification
              kind={validateKind}
              lowContrast
              hideCloseButton
              title={validateKind === 'success' ? 'Valid' : 'Invalid'}
              subtitle={validateMsg}
            />
          </div>
        )}
        <ToolBrowser project={name} onInsert={insertTool} />
      </Modal>

      {chatTmpl && (
        <AgentChatPanel
          title={`Test — ${chatTmpl.name}`}
          subtitle={chatTmpl.description}
          greeting={chatTmpl.greeting}
          chat={(m, t, cb, s) => chatWithTemplate(name, chatTmpl.name, m, t, cb, s)}
          onClose={() => setChatTmpl(null)}
        />
      )}
    </div>
  )
}
