import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { Button, InlineNotification, Loading, Tag, Tile } from '@carbon/react'
import { Chat, TrashCan } from '@carbon/icons-react'
import {
  listAgentCards,
  listAgentInstances,
  chatWithInstance,
  deleteAgentInstance,
  hasCapability,
  AgentCard,
  AgentInstance,
} from '../api/client'
import { useMe } from '../me'
import AgentChatPanel from '../components/AgentChatPanel'

export default function Agents() {
  const { name = '' } = useParams()
  const me = useMe()
  const canUse = hasCapability(me, name, 'ai-agents')

  const [cards, setCards] = useState<AgentCard[]>([])
  const [instances, setInstances] = useState<AgentInstance[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [chatCard, setChatCard] = useState<AgentCard | null>(null)

  const refresh = () =>
    Promise.all([listAgentCards(name), listAgentInstances(name)])
      .then(([c, i]) => {
        setCards(c.cards || [])
        setInstances(i.instances || [])
      })
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  const instanceFor = (template: string) => instances.find((i) => i.template === template)

  const remove = async (template: string) => {
    setBusy(template)
    setErr('')
    try {
      await deleteAgentInstance(name, template)
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading agents" />

  if (!canUse) {
    return (
      <div className="page">
        <h2>{name} — AI agents</h2>
        <InlineNotification
          kind="info"
          lowContrast
          hideCloseButton
          title="No access"
          subtitle="You do not have the ai-agents capability in this project. Ask a project admin to grant it on the members page."
        />
      </div>
    )
  }

  return (
    <div className="page">
      <h2 style={{ marginBottom: '0.5rem' }}>{name} — AI agents</h2>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0 0 1rem' }}>
        Launch an agent from a published template. Your session is private to you and starts on demand — just open a
        chat.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}

      {cards.length === 0 ? (
        <p style={{ color: 'var(--cds-text-secondary)' }}>
          No published agents yet. A project admin publishes agent templates, and they appear here as launchable cards.
        </p>
      ) : (
        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(20rem, 1fr))',
            gap: '1rem',
          }}
        >
          {cards.map((c) => {
            const inst = instanceFor(c.name)
            return (
              <Tile key={c.name} style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.5rem' }}>
                  <strong style={{ fontSize: '1rem' }}>{c.name}</strong>
                  {inst && (
                    <Tag type={inst.running ? 'green' : 'gray'} size="sm">
                      {inst.running ? 'running' : 'stopped'}
                    </Tag>
                  )}
                </div>
                <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.9rem', flexGrow: 1, minHeight: '2.5rem' }}>
                  {c.description || 'No description.'}
                </p>
                <div style={{ display: 'flex', gap: '0.375rem', flexWrap: 'wrap' }}>
                  {c.model && <Tag type="blue" size="sm">{c.model}</Tag>}
                  <Tag type="cool-gray" size="sm">{c.tool_count} tools</Tag>
                </div>
                <div style={{ display: 'flex', gap: '0.25rem' }}>
                  <Button kind="tertiary" size="sm" renderIcon={Chat} onClick={() => setChatCard(c)}>
                    Chat
                  </Button>
                  {inst && (
                    <Button
                      kind="danger--ghost"
                      size="sm"
                      renderIcon={TrashCan}
                      disabled={busy === c.name}
                      onClick={() => remove(c.name)}
                    >
                      {busy === c.name ? 'Stopping…' : 'Stop'}
                    </Button>
                  )}
                </div>
              </Tile>
            )
          })}
        </div>
      )}

      {chatCard && (
        <AgentChatPanel
          title={chatCard.name}
          subtitle={chatCard.description}
          greeting={chatCard.greeting}
          chat={(m, t, cb, s) => chatWithInstance(name, chatCard.name, m, t, cb, s)}
          onClose={() => {
            setChatCard(null)
            refresh()
          }}
        />
      )}
    </div>
  )
}
