import { useState } from 'react'
import {
  Modal,
  TextInput,
  NumberInput,
  Select,
  SelectItem,
  TextArea,
  InlineNotification,
} from '@carbon/react'
import { deployMcpServer, DeployMcpInput } from '../api/client'

// DeployMcpServerModal is the minimal "deploy an existing MCP server" form: the
// admin transcribes the image + run config from the server's Docker/K8s docs and
// the portal renders + runs it as a Nomad job. stdio is intentionally absent — it
// needs the auth wrapper (a later track).
export default function DeployMcpServerModal({
  open,
  onClose,
  onDeployed,
}: {
  open: boolean
  onClose: () => void
  onDeployed: () => void
}) {
  const [name, setName] = useState('')
  const [image, setImage] = useState('')
  const [transport, setTransport] = useState('sse')
  const [port, setPort] = useState(8080)
  const [path, setPath] = useState('')
  const [argsText, setArgsText] = useState('')
  const [envText, setEnvText] = useState('')
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
    setErr('')
  }

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
      modalHeading="Deploy MCP server"
      modalLabel="Platform Admin"
      primaryButtonText={busy ? 'Deploying…' : 'Deploy'}
      secondaryButtonText="Cancel"
      primaryButtonDisabled={busy || !name.trim() || !image.trim()}
      onRequestClose={() => {
        reset()
        onClose()
      }}
      onRequestSubmit={submit}
    >
      <p style={{ marginBottom: '1rem', color: 'var(--cds-text-secondary)' }}>
        Deploy an existing MCP server (internal or third-party) as a Nomad job — e.g.
        the HashiCorp Vault MCP server. Fill the fields from the server&apos;s container
        run instructions.
      </p>
      {err && (
        <InlineNotification
          kind="error"
          title="Deploy failed"
          subtitle={err}
          lowContrast
          onCloseButtonClick={() => setErr('')}
        />
      )}
      <TextInput
        id="mcp-name"
        labelText="Name"
        helperText="lowercase letters, digits, dashes (e.g. vault-mcp)"
        value={name}
        onChange={(e) => setName(e.target.value)}
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
          helperText="the port the server listens on in the container"
          min={1}
          max={65535}
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
