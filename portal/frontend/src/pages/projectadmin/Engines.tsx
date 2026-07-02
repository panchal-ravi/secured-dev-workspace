import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { Button, InlineNotification, TextInput, TextArea, Tag, Loading } from '@carbon/react'
import { getEngineStatus, setGithubCredentials, EngineStatus } from '../../api/client'

// Engines shows the project's engine-provisioning state and lets a project-admin set
// or update the GitHub App credentials. The standard engines (SSH CA, GitHub mount,
// LLM virtual key, WIF policies, Boundary credential store + SSH-cert library) are
// provisioned AUTOMATICALLY when the project is created; only the GitHub App config is
// deferred to here. The private key is write-only — it goes straight to Vault and is
// never stored or echoed back.
export default function Engines() {
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
      .then(setStatus)
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

  return (
    <div className="page" style={{ maxWidth: 720 }}>
      <h2>{name} — engines</h2>
      <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '1rem' }}>
        The standard secret engines (Vault SSH CA, GitHub App broker, LiteLLM virtual key, workspace
        WIF read policies, and the Boundary credential store + SSH-certificate library) are provisioned
        automatically when the project is created. Supply the GitHub App credentials below to enable
        git push; the private key is write-only — it is sent once to Vault and never stored here.
      </p>

      {status && (
        <div style={{ marginBottom: '1.5rem', display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
          <Tag type={status.provisioned ? 'green' : 'gray'}>
            Engines: {status.provisioned ? 'provisioned' : status.status}
          </Tag>
          <Tag type={status.github_configured ? 'green' : 'red'}>
            GitHub App: {status.github_configured ? 'configured' : 'not configured'}
          </Tag>
          {status.credential_library_id && <Tag type="blue">Boundary cred library set</Tag>}
        </div>
      )}

      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      {ok && (
        <InlineNotification kind="success" title="Done" subtitle={ok} lowContrast onCloseButtonClick={() => setOk('')} />
      )}

      <h4 style={{ marginTop: '1rem' }}>
        {status?.github_configured ? 'Update GitHub App credentials' : 'Set GitHub App credentials'}
      </h4>
      <TextInput
        id="app-id"
        labelText="GitHub App ID"
        style={{ marginTop: '1rem' }}
        value={appID}
        onChange={(e) => setAppID(e.target.value)}
      />
      <TextInput
        id="install-id"
        labelText="GitHub App installation ID"
        style={{ marginTop: '1rem' }}
        value={installID}
        onChange={(e) => setInstallID(e.target.value)}
      />
      <TextArea
        id="priv-key"
        labelText="GitHub App private key (PKCS#1 PEM)"
        rows={8}
        style={{ marginTop: '1rem' }}
        value={privKey}
        onChange={(e) => setPrivKey(e.target.value)}
        placeholder={'-----BEGIN RSA PRIVATE KEY-----\n…\n-----END RSA PRIVATE KEY-----'}
      />
      <TextInput
        id="repos"
        labelText="Repositories (optional; comma/newline-separated — else all installed repos)"
        style={{ marginTop: '1rem' }}
        value={repos}
        onChange={(e) => setRepos(e.target.value)}
      />
      <Button style={{ marginTop: '1.5rem' }} disabled={!valid || busy} onClick={submit}>
        {busy ? 'Saving…' : 'Save GitHub credentials'}
      </Button>
    </div>
  )
}
