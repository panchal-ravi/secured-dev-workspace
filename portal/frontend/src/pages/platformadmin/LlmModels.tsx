import { useEffect, useState } from 'react'
import {
  Button,
  Form,
  InlineNotification,
  Loading,
  PasswordInput,
  TextInput,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
} from '@carbon/react'
import {
  listLlmModels,
  onboardLlmModel,
  setProviderKey,
  testLlmModel,
  publishLlmModel,
  deleteLlmModel,
  LlmModel,
} from '../../api/client'

const statusTag: Record<string, 'gray' | 'blue' | 'green'> = {
  draft: 'blue',
  published: 'green',
}

export default function LlmModels() {
  const [models, setModels] = useState<LlmModel[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')

  const [name, setName] = useState('')
  const [provider, setProvider] = useState('')
  const [backend, setBackend] = useState('')

  // Write-only provider-key form (the key is never read back).
  const [keyProvider, setKeyProvider] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [keyNotice, setKeyNotice] = useState('')

  const refresh = () =>
    listLlmModels()
      .then(setModels)
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

  const onboard = () =>
    run('__onboard__', async () => {
      await onboardLlmModel({ name: name.trim(), provider: provider.trim(), backend_model: backend.trim() })
      setName('')
      setProvider('')
      setBackend('')
    })

  const saveKey = async () => {
    const p = keyProvider.trim()
    setBusy('__key__')
    setErr('')
    setKeyNotice('')
    try {
      await setProviderKey({ provider: p, api_key: apiKey })
      setApiKey('')
      setKeyNotice(`Saved API key for provider "${p}".`)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading models" />

  return (
    <div className="page">
      <h2 style={{ marginBottom: '0.5rem' }}>LLM models</h2>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Onboard a model into the LLM gateway, verify it with a scoped budgeted key (the
        way a project consumes it), then publish so projects can select it.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {keyNotice && (
        <InlineNotification kind="success" title="Saved" subtitle={keyNotice} lowContrast onCloseButtonClick={() => setKeyNotice('')} />
      )}

      <p style={{ color: 'var(--cds-text-secondary)', margin: '0 0 0.5rem' }}>
        Set a provider API key before onboarding its first model. The key is stored
        write-only in Vault and injected at call time — it is never displayed again.
      </p>
      <Form
        onSubmit={(e) => {
          e.preventDefault()
          saveKey()
        }}
        style={{ display: 'flex', gap: '1rem', alignItems: 'flex-end', marginBottom: '1.5rem', flexWrap: 'wrap' }}
      >
        <TextInput id="key-provider" labelText="Provider" placeholder="deepseek" value={keyProvider} onChange={(e) => setKeyProvider(e.target.value)} />
        <PasswordInput id="key-value" labelText="API key" placeholder="sk-…" value={apiKey} onChange={(e) => setApiKey(e.target.value)} />
        <Button type="submit" kind="secondary" disabled={busy === '__key__' || !keyProvider.trim() || !apiKey}>
          Save key
        </Button>
      </Form>

      <Form
        onSubmit={(e) => {
          e.preventDefault()
          onboard()
        }}
        style={{ display: 'flex', gap: '1rem', alignItems: 'flex-end', marginBottom: '1.5rem', flexWrap: 'wrap' }}
      >
        <TextInput id="llm-name" labelText="Model name" placeholder="deepseek-v4-flash" value={name} onChange={(e) => setName(e.target.value)} />
        <TextInput id="llm-provider" labelText="Provider" placeholder="deepseek" value={provider} onChange={(e) => setProvider(e.target.value)} />
        <TextInput id="llm-backend" labelText="Backend model" placeholder="deepseek/deepseek-chat" value={backend} onChange={(e) => setBackend(e.target.value)} />
        <Button type="submit" disabled={busy === '__onboard__' || !name.trim() || !provider.trim() || !backend.trim()}>
          Onboard
        </Button>
      </Form>

      {models.length === 0 ? (
        <p>No models found in the gateway.</p>
      ) : (
        <TableContainer
          title="Gateway inventory"
          description="Models the LiteLLM gateway serves. Managed models were onboarded here; unmanaged ones come from the gateway config or were added directly."
        >
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
                <TableHeader>Source</TableHeader>
                <TableHeader>Provider</TableHeader>
                <TableHeader>Backend</TableHeader>
                <TableHeader>Status</TableHeader>
                <TableHeader>Test</TableHeader>
                <TableHeader>Actions</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {models.map((m) => {
                const tested = m.test_result?.passed
                return (
                  <TableRow key={m.name}>
                    <TableCell>{m.name}</TableCell>
                    <TableCell>
                      {m.orphaned ? (
                        <Tag type="red">orphaned</Tag>
                      ) : (
                        <Tag type={m.source === 'config' ? 'purple' : 'cyan'}>{m.source || 'db'}</Tag>
                      )}
                    </TableCell>
                    <TableCell>{m.provider || '—'}</TableCell>
                    <TableCell>{m.backend_model || '—'}</TableCell>
                    <TableCell>
                      {m.managed ? (
                        <Tag type={statusTag[m.status || ''] || 'gray'}>{m.status}</Tag>
                      ) : (
                        <Tag type="gray">unmanaged</Tag>
                      )}
                    </TableCell>
                    <TableCell>
                      {!m.managed ? (
                        <span style={{ color: 'var(--cds-text-secondary)' }}>—</span>
                      ) : m.test_result ? (
                        <Tag type={tested ? 'green' : 'red'}>{tested ? 'passed' : 'failed'}</Tag>
                      ) : (
                        <Tag type="gray">untested</Tag>
                      )}
                    </TableCell>
                    <TableCell>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
                        {m.managed && !m.orphaned && (
                          <>
                            <Button size="sm" kind="tertiary" disabled={busy === m.name} onClick={() => run(m.name, () => testLlmModel(m.name))}>
                              Test
                            </Button>
                            <Button
                              size="sm"
                              kind="primary"
                              disabled={busy === m.name || !tested || m.status === 'published'}
                              onClick={() => run(m.name, () => publishLlmModel(m.name))}
                            >
                              Publish
                            </Button>
                          </>
                        )}
                        {m.managed ? (
                          <Button size="sm" kind="danger--ghost" disabled={busy === m.name} onClick={() => run(m.name, () => deleteLlmModel(m.name))}>
                            {m.orphaned ? 'Remove' : 'Delete'}
                          </Button>
                        ) : (
                          <span style={{ color: 'var(--cds-text-secondary)' }}>read-only</span>
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
    </div>
  )
}
