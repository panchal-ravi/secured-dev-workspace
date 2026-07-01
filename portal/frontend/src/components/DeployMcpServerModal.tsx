import { useEffect, useState } from 'react'
import {
  Modal,
  TextInput,
  NumberInput,
  Select,
  SelectItem,
  TextArea,
  Checkbox,
  InlineNotification,
} from '@carbon/react'
import { deployMcpServer, DeployMcpInput, McpServer } from '../api/client'

// DeployMcpServerModal is the minimal "deploy an existing MCP server" form: the
// admin transcribes the image + run config from the server's Docker/K8s docs and
// the portal renders + runs it as a Nomad job. stdio is intentionally absent — it
// needs the auth wrapper (a later track). Passing `initial` puts the form in edit
// mode: fields prefill from the existing server, the name is locked (it is the
// identity/job key), and saving re-registers the job in place (version bump).
export default function DeployMcpServerModal({
  open,
  onClose,
  onDeployed,
  initial,
}: {
  open: boolean
  onClose: () => void
  onDeployed: () => void
  initial?: McpServer | null
}) {
  const editing = !!initial
  const [name, setName] = useState('')
  const [image, setImage] = useState('')
  const [transport, setTransport] = useState('sse')
  const [port, setPort] = useState(8080)
  const [path, setPath] = useState('')
  const [argsText, setArgsText] = useState('')
  const [envText, setEnvText] = useState('')
  const [injectVaultToken, setInjectVaultToken] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const reset = () => {
    setName('')
    setImage('')
    setTransport('sse')
    setPort(8080)
    setPath('')
    setArgsText('')
    setEnvText('')
    setInjectVaultToken(false)
    setErr('')
  }

  // Sync form state each time the modal opens: prefill from `initial` in edit mode,
  // otherwise start blank. Keyed on open/initial so reopening a row always reflects
  // its current values.
  useEffect(() => {
    if (!open) return
    if (initial) {
      setName(initial.name)
      setImage(initial.image)
      setTransport(initial.transport)
      setPort(initial.port)
      setPath(initial.path ?? '')
      setArgsText((initial.command ?? []).join('\n'))
      setEnvText(
        Object.entries(initial.env ?? {})
          .map(([k, v]) => `${k}=${v}`)
          .join('\n'),
      )
      setInjectVaultToken(!!initial.inject_vault_token)
      setErr('')
    } else {
      reset()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, initial])

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      const input: DeployMcpInput = {
        name: name.trim(),
        image: image.trim(),
        transport,
        port,
      }
      if (path.trim()) input.path = path.trim()
      const args = argsText
        .split('\n')
        .map((l) => l.trim())
        .filter(Boolean)
      if (args.length) input.command = args
      const env = parseEnv(envText)
      if (Object.keys(env).length) input.env = env
      if (injectVaultToken) input.inject_vault_token = true
      // The form doesn't expose secret_refs; preserve any set out-of-band on edit.
      if (editing && initial?.secret_refs) input.secret_refs = initial.secret_refs

      await deployMcpServer(input)
      reset()
      onDeployed()
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
      modalHeading={editing ? 'Edit MCP server' : 'Deploy MCP server'}
      modalLabel="Platform Admin"
      primaryButtonText={busy ? (editing ? 'Saving…' : 'Deploying…') : editing ? 'Save changes' : 'Deploy'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy || !name.trim() || !image.trim()}
      onRequestClose={() => {
        reset()
        onClose()
      }}
      onRequestSubmit={submit}
    >
      <p style={{ marginBottom: '1rem', color: 'var(--cds-text-secondary)' }}>
        {editing
          ? 'Edit the run config and save. This re-registers the Nomad job in place and resets it to deployed — re-run Test (and Publish) afterwards.'
          : 'Deploy an existing MCP server (internal or third-party) as a Nomad job — e.g. the HashiCorp Vault MCP server. Fill the fields from the server’s container run instructions.'}
      </p>
      {err && (
        <InlineNotification
          kind="error"
          title={editing ? 'Save failed' : 'Deploy failed'}
          subtitle={err}
          lowContrast
          onCloseButtonClick={() => setErr('')}
        />
      )}
      <TextInput
        id="mcp-name"
        labelText="Name"
        helperText={editing ? 'the server identity — not editable' : 'lowercase letters, digits, dashes (e.g. vault-mcp)'}
        value={name}
        onChange={(e) => setName(e.target.value)}
        disabled={editing}
      />
      <TextInput
        id="mcp-image"
        labelText="Container image"
        placeholder="hashicorp/vault-mcp-server:latest"
        value={image}
        onChange={(e) => setImage(e.target.value)}
        style={{ marginTop: '1rem' }}
      />
      <div style={{ display: 'flex', gap: '1rem', marginTop: '1rem' }}>
        <Select
          id="mcp-transport"
          labelText="Transport"
          value={transport}
          onChange={(e) => setTransport(e.target.value)}
        >
          <SelectItem value="sse" text="SSE" />
          <SelectItem value="streamable-http" text="Streamable HTTP" />
        </Select>
        <NumberInput
          id="mcp-port"
          label="Listen port"
          helperText="the port the server listens on in the container — must be 8080–8099 (the band the gateway can reach on agent nodes)"
          min={8080}
          max={8099}
          value={port}
          onChange={(_e, { value }) => setPort(Number(value))}
        />
      </div>
      <TextInput
        id="mcp-path"
        labelText="MCP path (optional)"
        helperText="defaults to /sse or /mcp by transport"
        value={path}
        onChange={(e) => setPath(e.target.value)}
        style={{ marginTop: '1rem' }}
      />
      <TextArea
        id="mcp-args"
        labelText="Arguments (optional, one per line)"
        helperText="one token per line — e.g. a binary and its subcommand go on separate lines, not one line with a space"
        placeholder={'/bin/vault-mcp-server\nhttp'}
        value={argsText}
        onChange={(e) => setArgsText(e.target.value)}
        rows={3}
        style={{ marginTop: '1rem' }}
      />
      <TextArea
        id="mcp-env"
        labelText="Environment (optional, KEY=VALUE per line)"
        helperText="non-secret values only; secrets are injected from Vault"
        value={envText}
        onChange={(e) => setEnvText(e.target.value)}
        rows={3}
        style={{ marginTop: '1rem' }}
      />
      <Checkbox
        id="mcp-inject-vault-token"
        labelText="Inject a Vault (WIF) token as VAULT_TOKEN"
        helperText="For servers that authenticate to Vault, e.g. the Vault MCP server. Nomad mints a powerless self-test token so the consumption-mirror test can run; real access comes from the project blueprint."
        checked={injectVaultToken}
        onChange={(_e, { checked }) => setInjectVaultToken(checked)}
        style={{ marginTop: '1rem' }}
      />
    </Modal>
  )
}

function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const eq = trimmed.indexOf('=')
    if (eq <= 0) continue
    out[trimmed.slice(0, eq).trim()] = trimmed.slice(eq + 1).trim()
  }
  return out
}
