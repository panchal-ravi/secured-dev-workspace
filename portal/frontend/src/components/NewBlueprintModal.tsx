import { useState } from 'react'
import {
  Modal, Select, SelectItem, TextInput, TextArea, NumberInput, Checkbox, Button, FormGroup,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import {
  createBlueprint, BlueprintManifest, ParamSpec, EngineSpec, RoleSpec,
} from '../api/client'

type Cls = 'A' | 'B' | 'C'

// Per-class working defaults, taken verbatim from the checked-in seeds
// (backend internal/blueprint/seeds/class{A,B,C}-*.json) so a new author starts
// from a known-good shape.
interface Defaults {
  description: string
  policyTpl: string
  wifNameTpl: string
  engines: EngineSpec[]
  role: RoleSpec | null
  params: ParamSpec[]
  envTemplates: { key: string; value: string }[]
}

const DEFAULTS: Record<Cls, Defaults> = {
  A: {
    description: 'Postgres MCP server — dynamic SELECT-only DB creds brokered by Vault.',
    policyTpl: 'path "{{.Mount}}/creds/{{.Role}}" {\n  capabilities = ["read"]\n}\n',
    wifNameTpl: 'mcp-postgres-mcp',
    engines: [{ type: 'database', plugin: 'postgresql-database-plugin', mount_path_tpl: 'database/{{.Namespace}}-pg' }],
    role: {
      name_tpl: 'mcp-ro',
      creation_statements: [
        'CREATE ROLE "{{name}}" WITH LOGIN PASSWORD \'{{password}}\' VALID UNTIL \'{{expiration}}\';',
        'GRANT USAGE ON SCHEMA public TO "{{name}}";',
        'GRANT SELECT ON ALL TABLES IN SCHEMA public TO "{{name}}";',
      ],
      default_ttl_seconds: 3600,
      max_ttl_seconds: 86400,
    },
    params: [
      { name: 'connection_url', type: 'string', required: true, prompt: 'Postgres connection URL with {{username}}/{{password}} templating' },
      { name: 'bootstrap_username', type: 'string', required: true, prompt: 'Bootstrap admin username (rotated immediately)' },
      { name: 'bootstrap_password', type: 'secret', required: true, prompt: 'Bootstrap admin password (write-only; rotated immediately)' },
      { name: 'db_host', type: 'string', required: true, prompt: 'Database host the MCP server connects to' },
      { name: 'db_port', type: 'string', required: true, prompt: 'Database port' },
      { name: 'db_name', type: 'string', required: true, prompt: 'Database name' },
    ],
    envTemplates: [
      { key: 'DATABASE_URI', value: '{{ with secret "${cred_path}" }}postgresql://{{ .Data.username }}:{{ .Data.password }}@${db_host}:${db_port}/${db_name}{{ end }}' },
    ],
  },
  B: {
    description: 'Generic upstream-API-key MCP server — write-only key seeded into project KV.',
    policyTpl: 'path "{{.Mount}}/data/projects/generic-api-key" {\n  capabilities = ["read"]\n}\n',
    wifNameTpl: 'mcp-generic-api-key',
    engines: [],
    role: null,
    params: [{ name: 'api_key', type: 'secret', required: true, prompt: 'Upstream API key (write-only)' }],
    envTemplates: [{ key: 'API_KEY', value: '{{ with secret "${cred_path}" }}{{ .Data.data.api_key }}{{ end }}' }],
  },
  C: {
    description: 'HashiCorp Vault MCP server — VAULT_TOKEN is itself the WIF-minted credential.',
    policyTpl: 'path "{{.Mount}}/data/projects/*" {\n  capabilities = ["read"]\n}\n',
    wifNameTpl: 'mcp-vault-mcp',
    engines: [],
    role: null,
    params: [],
    envTemplates: [],
  },
}

export default function NewBlueprintModal({
  open,
  onClose,
  onCreated,
}: {
  open: boolean
  onClose: () => void
  onCreated: () => void
}) {
  const [cls, setCls] = useState<Cls>('A')
  const [id, setId] = useState('')
  const [version, setVersion] = useState(1)
  const [d, setD] = useState<Defaults>(DEFAULTS.A)
  const [allowExtraGrants, setAllowExtraGrants] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const pickClass = (c: Cls) => {
    setCls(c)
    setD(DEFAULTS[c])
  }

  const setParam = (i: number, patch: Partial<ParamSpec>) =>
    setD((s) => ({ ...s, params: s.params.map((p, j) => (j === i ? { ...p, ...patch } : p)) }))
  const addParam = () =>
    setD((s) => ({ ...s, params: [...s.params, { name: '', type: 'string', required: true, prompt: '' }] }))
  const removeParam = (i: number) => setD((s) => ({ ...s, params: s.params.filter((_, j) => j !== i) }))

  const setEnv = (i: number, patch: Partial<{ key: string; value: string }>) =>
    setD((s) => ({ ...s, envTemplates: s.envTemplates.map((e, j) => (j === i ? { ...e, ...patch } : e)) }))
  const addEnv = () => setD((s) => ({ ...s, envTemplates: [...s.envTemplates, { key: '', value: '' }] }))
  const removeEnv = (i: number) => setD((s) => ({ ...s, envTemplates: s.envTemplates.filter((_, j) => j !== i) }))

  const assemble = (): BlueprintManifest => {
    const env_templates: Record<string, string> = {}
    for (const e of d.envTemplates) if (e.key.trim()) env_templates[e.key.trim()] = e.value
    const m: BlueprintManifest = {
      id: id.trim(),
      version,
      class: cls,
      description: d.description,
      policy_tpl: d.policyTpl,
      wif_role: { name_tpl: d.wifNameTpl, token_ttl: '1h' },
      params: d.params,
      allow_extra_grants: allowExtraGrants,
    }
    if (cls === 'A') {
      m.engines = d.engines
      m.role = d.role || undefined
    }
    if (Object.keys(env_templates).length > 0) m.job_credential = { env_templates }
    return m
  }

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await createBlueprint(assemble())
      onCreated()
      onClose()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      modalHeading="New Vault blueprint"
      modalLabel="Vault credential blueprint"
      primaryButtonText={busy ? 'Creating…' : 'Create draft'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy || !id.trim()}
      onRequestClose={onClose}
      onRequestSubmit={submit}
      size="lg"
    >
      {err && <p style={{ color: 'var(--cds-text-error)', marginBottom: '1rem' }}>{err}</p>}

      <FormGroup legendText="Class">
        <Select id="bp-class" labelText="Credential class" value={cls} onChange={(e) => pickClass(e.target.value as Cls)}>
          <SelectItem value="A" text="A — dynamic broker (database engine mints short-lived creds)" />
          <SelectItem value="B" text="B — static upstream secret (write-only KV seed)" />
          <SelectItem value="C" text="C — vault-token (the WIF token is the credential)" />
        </Select>
      </FormGroup>

      <div style={{ display: 'flex', gap: '1rem', marginTop: '1rem' }}>
        <TextInput id="bp-id" labelText="ID" placeholder="postgres-mcp" value={id} onChange={(e) => setId(e.target.value)} />
        <NumberInput id="bp-version" label="Version" min={1} value={version} onChange={(_e, { value }) => setVersion(Number(value) || 1)} />
      </div>

      <TextInput
        id="bp-desc"
        style={{ marginTop: '1rem' }}
        labelText="Description"
        value={d.description}
        onChange={(e) => setD((s) => ({ ...s, description: e.target.value }))}
      />

      <TextInput
        id="bp-wif"
        style={{ marginTop: '1rem' }}
        labelText="WIF role name template"
        helperText="The Nomad-WIF role the MCP job binds (token TTL fixed at 1h)."
        value={d.wifNameTpl}
        onChange={(e) => setD((s) => ({ ...s, wifNameTpl: e.target.value }))}
      />

      <TextArea
        id="bp-policy"
        style={{ marginTop: '1rem' }}
        labelText="Policy template (HCL)"
        helperText="Rendered per-project at deploy: {{.Mount}} / {{.Namespace}} / {{.Role}}."
        rows={4}
        value={d.policyTpl}
        onChange={(e) => setD((s) => ({ ...s, policyTpl: e.target.value }))}
      />

      {cls === 'A' && d.role && (
        <FormGroup legendText="Database engine + role (Class A)" style={{ marginTop: '1rem' }}>
          <TextInput
            id="bp-engine-mount"
            labelText="Engine mount path template"
            value={d.engines[0]?.mount_path_tpl || ''}
            onChange={(e) => setD((s) => ({ ...s, engines: [{ ...s.engines[0], mount_path_tpl: e.target.value }] }))}
          />
          <TextInput
            id="bp-engine-plugin"
            style={{ marginTop: '1rem' }}
            labelText="Database plugin"
            value={d.engines[0]?.plugin || ''}
            onChange={(e) => setD((s) => ({ ...s, engines: [{ ...s.engines[0], plugin: e.target.value }] }))}
          />
          <TextInput
            id="bp-role-name"
            style={{ marginTop: '1rem' }}
            labelText="Role name template"
            value={d.role.name_tpl}
            onChange={(e) => setD((s) => ({ ...s, role: { ...s.role!, name_tpl: e.target.value } }))}
          />
          <TextArea
            id="bp-role-stmts"
            style={{ marginTop: '1rem' }}
            labelText="Creation statements (one SQL statement per line)"
            rows={4}
            value={d.role.creation_statements.join('\n')}
            onChange={(e) =>
              setD((s) => ({
                ...s,
                role: { ...s.role!, creation_statements: e.target.value.split('\n').filter((l) => l.trim()) },
              }))
            }
          />
          <div style={{ display: 'flex', gap: '1rem', marginTop: '1rem' }}>
            <NumberInput
              id="bp-role-ttl"
              label="Default TTL (s)"
              min={1}
              value={d.role.default_ttl_seconds}
              onChange={(_e, { value }) => setD((s) => ({ ...s, role: { ...s.role!, default_ttl_seconds: Number(value) || 0 } }))}
            />
            <NumberInput
              id="bp-role-maxttl"
              label="Max TTL (s)"
              min={1}
              value={d.role.max_ttl_seconds}
              onChange={(_e, { value }) => setD((s) => ({ ...s, role: { ...s.role!, max_ttl_seconds: Number(value) || 0 } }))}
            />
          </div>
        </FormGroup>
      )}

      <FormGroup legendText="Parameters" style={{ marginTop: '1rem' }}>
        {d.params.map((p, i) => (
          <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '0.5rem' }}>
            <TextInput id={`p-name-${i}`} labelText="Name" value={p.name} onChange={(e) => setParam(i, { name: e.target.value })} />
            <Select id={`p-type-${i}`} labelText="Type" value={p.type} onChange={(e) => setParam(i, { type: e.target.value as ParamSpec['type'] })}>
              <SelectItem value="string" text="string" />
              <SelectItem value="int" text="int" />
              <SelectItem value="secret" text="secret" />
            </Select>
            <TextInput id={`p-prompt-${i}`} labelText="Prompt" value={p.prompt || ''} onChange={(e) => setParam(i, { prompt: e.target.value })} />
            <Checkbox id={`p-req-${i}`} labelText="Required" checked={p.required} onChange={(_e, { checked }) => setParam(i, { required: checked })} />
            <Button hasIconOnly renderIcon={TrashCan} iconDescription="Remove" kind="danger--ghost" size="md" onClick={() => removeParam(i)} />
          </div>
        ))}
        <Button renderIcon={Add} kind="ghost" size="sm" onClick={addParam}>
          Add parameter
        </Button>
        {(cls === 'A' || cls === 'B') && !d.params.some((p) => p.type === 'secret') && (
          <p style={{ color: 'var(--cds-text-error)', marginTop: '0.5rem' }}>
            Class {cls} requires at least one <code>secret</code> parameter.
          </p>
        )}
      </FormGroup>

      <FormGroup legendText="Job credential env templates" style={{ marginTop: '1rem' }}>
        {d.envTemplates.map((e, i) => (
          <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '0.5rem' }}>
            <TextInput id={`e-key-${i}`} labelText="Env var" value={e.key} onChange={(ev) => setEnv(i, { key: ev.target.value })} />
            <TextInput id={`e-val-${i}`} labelText="Template" value={e.value} onChange={(ev) => setEnv(i, { value: ev.target.value })} />
            <Button hasIconOnly renderIcon={TrashCan} iconDescription="Remove" kind="danger--ghost" size="md" onClick={() => removeEnv(i)} />
          </div>
        ))}
        <Button renderIcon={Add} kind="ghost" size="sm" onClick={addEnv}>
          Add env template
        </Button>
        {(cls === 'A' || cls === 'B') && d.envTemplates.filter((e) => e.key.trim()).length === 0 && (
          <p style={{ color: 'var(--cds-text-error)', marginTop: '0.5rem' }}>
            Class {cls} requires at least one job-credential env template.
          </p>
        )}
      </FormGroup>

      <FormGroup legendText="Deploy-time grants" style={{ marginTop: '1rem' }}>
        <Checkbox
          id="bp-allow-grants"
          labelText="Allow project-admins to attach additional Vault path grants at deploy time"
          checked={allowExtraGrants}
          onChange={(_e, { checked }) => setAllowExtraGrants(checked)}
        />
        <p style={{ color: 'var(--cds-text-secondary)', marginTop: '0.25rem', fontSize: '0.8rem' }}>
          Grants are confined to the project&apos;s own Vault namespace and linted (no
          sys/auth/identity/cubbyhole, no sudo/root). Off by default.
        </p>
      </FormGroup>
    </Modal>
  )
}
