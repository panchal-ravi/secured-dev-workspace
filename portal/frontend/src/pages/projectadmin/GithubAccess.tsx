import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { Button, InlineNotification, TextInput, TextArea, Tag, Loading, Stack } from '@carbon/react'
import { getEngineStatus, setGithubCredentials, EngineStatus } from '../../api/client'

// GithubAccess configures the project's GitHub secrets engine so every new workspace
// receives a short-lived GitHub PAT minted by Vault (git push works without any pasted
// credential). The standard services (SSH CA, GitHub mount, LLM virtual key, WIF
// policies, Boundary credential store + SSH-cert library) are provisioned AUTOMATICALLY
// when the project is created; only the GitHub App config is deferred to here. The
// private key is write-only — it goes straight to Vault and is never stored or echoed
// back; the other App coordinates prefill from the descriptor.
export default function GithubAccess() {
  const { name = '' } = useParams()
  const [status, setStatus] = useState<EngineStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [appID, setAppID] = useState('')
  const [installID, setInstallID] = useState('')
  const [privKey, setPrivKey] = useState('')
  const [repos, setRepos] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [ok, setOk] = useState('')

  const refresh = () =>
    getEngineStatus(name)
      .then((s) => {
        setStatus(s)
        // Prefill the non-secret saved coordinates (the key is never echoed back).
        if (s.github_app_id) setAppID(String(s.github_app_id))
        if (s.github_app_installation_id) setInstallID(String(s.github_app_installation_id))
        if (s.github_repositories?.length) setRepos(s.github_repositories.join(', '))
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  const submit = async () => {
    setBusy(true)
    setErr('')
    setOk('')
    try {
      await setGithubCredentials(name, {
        github_app_id: Number(appID),
        github_app_installation_id: Number(installID),
        github_app_private_key: privKey,
        github_repositories: repos
          .split(/[\n,]/)
          .map((s) => s.trim())
          .filter(Boolean),
      })
      setOk('GitHub App credentials saved to Vault (write-only). git push will now work in this project’s workspaces.')
      setPrivKey('')
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const valid = appID.trim() && installID.trim() && privKey.trim() && !Number.isNaN(Number(appID)) && !Number.isNaN(Number(installID))

  if (loading) return <Loading withOverlay description="Loading engine status" />

  // The standard engines are provisioned atomically at project-create, so their
  // state is the single `provisioned` flag; GitHub App config is the one deferred step.
  const engines = status
    ? [
        { label: 'Vault SSH CA (ssh/)', ok: status.provisioned, detail: 'signs workspace host + client certs' },
        {
          label: 'GitHub App broker (github/)',
          ok: status.provisioned && status.github_configured,
          detail: status.github_configured ? 'App configured — mints ephemeral push tokens' : 'mount ready — supply the App credentials below',
        },
        { label: 'LiteLLM virtual key', ok: status.provisioned, detail: 'scoped key in secret/projects/llm' },
        { label: 'Workspace WIF read policies', ok: status.provisioned, detail: '5 policies bound to the workspace identity' },
        {
          label: 'Boundary credential store + SSH-cert library',
          ok: status.provisioned && !!status.credential_library_id,
          detail: status.credential_library_id || 'pending',
        },
      ]
    : []

  return (
    <div className="page" style={{ maxWidth: 720 }}>
      <h2>{name} — GitHub access</h2>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        Supply the GitHub App credentials below to configure the project&apos;s GitHub secrets engine:
        every new workspace then receives a short-lived GitHub PAT minted by Vault, so git push works
        without any pasted credential. The private key is write-only — it is sent once to Vault and
        never stored here (the other fields prefill from the last save). The standard project services
        are provisioned automatically when the project is created.
      </p>

      {status && (
        <div style={{ marginBottom: '1.5rem' }}>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.75rem' }}>
            <Tag type={status.provisioned ? 'green' : 'gray'}>
              Auto-provisioned services: {status.provisioned ? 'provisioned' : status.status}
            </Tag>
            <Tag type={status.github_configured ? 'green' : 'red'}>
              GitHub App: {status.github_configured ? 'configured' : 'not configured'}
            </Tag>
          </div>
          <ul style={{ borderLeft: '3px solid var(--cds-border-subtle)', paddingLeft: '1rem', display: 'grid', gap: '0.375rem' }}>
            {engines.map((e) => (
              <li key={e.label} style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem' }}>
                <Tag size="sm" type={e.ok ? 'green' : 'gray'}>
                  {e.ok ? 'ready' : 'pending'}
                </Tag>
                <span>{e.label}</span>
                <span style={{ color: 'var(--cds-text-secondary)', fontSize: '0.75rem' }}>{e.detail}</span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {ok && (
        <InlineNotification kind="success" title="Done" subtitle={ok} lowContrast onCloseButtonClick={() => setOk('')} />
      )}

      <h4 style={{ margin: '1rem 0' }}>
        {status?.github_configured ? 'Update GitHub App credentials' : 'Set GitHub App credentials'}
      </h4>
      <Stack gap={5}>
        <TextInput id="app-id" labelText="GitHub App ID" value={appID} onChange={(e) => setAppID(e.target.value)} />
        <TextInput
          id="install-id"
          labelText="GitHub App installation ID"
          value={installID}
          onChange={(e) => setInstallID(e.target.value)}
        />
        <TextArea
          id="priv-key"
          labelText={
            status?.github_configured
              ? 'GitHub App private key (PKCS#1 PEM) — saved; paste again to update'
              : 'GitHub App private key (PKCS#1 PEM)'
          }
          rows={8}
          value={privKey}
          onChange={(e) => setPrivKey(e.target.value)}
          placeholder={'-----BEGIN RSA PRIVATE KEY-----\n…\n-----END RSA PRIVATE KEY-----'}
        />
        <TextInput
          id="repos"
          labelText="Repository names (optional, comma-separated, e.g. my-repo — URLs are accepted and reduced to the name; empty = all installed repos)"
          value={repos}
          onChange={(e) => setRepos(e.target.value)}
        />
        <Button disabled={!valid || busy} onClick={submit}>
          {busy ? 'Saving…' : 'Save GitHub credentials'}
        </Button>
      </Stack>
    </div>
  )
}
