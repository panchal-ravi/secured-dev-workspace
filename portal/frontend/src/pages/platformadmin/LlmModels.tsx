import { useEffect, useState } from 'react'
import {
  Button,
  Form,
  InlineNotification,
  Loading,
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
        <p>No models onboarded yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Name</TableHeader>
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
                    <TableCell>{m.provider}</TableCell>
                    <TableCell>{m.backend_model}</TableCell>
                    <TableCell>
                      <Tag type={statusTag[m.status] || 'gray'}>{m.status}</Tag>
                    </TableCell>
                    <TableCell>
                      {m.test_result ? (
                        <Tag type={tested ? 'green' : 'red'}>{tested ? 'passed' : 'failed'}</Tag>
                      ) : (
                        <Tag type="gray">untested</Tag>
                      )}
                    </TableCell>
                    <TableCell>
                      <div style={{ display: 'flex', gap: '0.5rem' }}>
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
                        <Button size="sm" kind="danger--ghost" disabled={busy === m.name} onClick={() => run(m.name, () => deleteLlmModel(m.name))}>
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
    </div>
  )
}
