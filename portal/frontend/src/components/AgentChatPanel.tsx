import { useEffect, useRef, useState } from 'react'
import { Button, Modal, TextInput } from '@carbon/react'
import { SendAlt } from '@carbon/icons-react'
import { AgentChatEvent } from '../api/client'
import Markdown from './Markdown'

interface Message {
  role: 'user' | 'agent'
  text: string
}

// ChatFn streams one chat turn: it POSTs the message on threadId and invokes onEvent
// per SSE event. Both the template test-chat and per-user instance chat supply one.
export type ChatFn = (
  message: string,
  threadId: string,
  onEvent: (ev: AgentChatEvent) => void,
  signal?: AbortSignal,
) => Promise<void>

// newThreadId mints a client-side conversation id. The agent keeps per-thread
// memory in an in-process checkpointer, so reusing the id continues the conversation.
function newThreadId(): string {
  if (typeof crypto !== 'undefined' && crypto.randomUUID) return crypto.randomUUID()
  return 'thread-' + Math.random().toString(36).slice(2)
}

export default function AgentChatPanel({
  title,
  subtitle,
  greeting,
  chat,
  onClose,
}: {
  title: string
  subtitle?: string
  greeting?: string
  chat: ChatFn
  onClose: () => void
}) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [tool, setTool] = useState('') // currently-running tool name, if any
  const [threadId, setThreadId] = useState(newThreadId)
  const [err, setErr] = useState('')
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, tool])

  const send = async () => {
    const text = input.trim()
    if (!text || streaming) return
    setInput('')
    setErr('')
    setMessages((m) => [...m, { role: 'user', text }, { role: 'agent', text: '' }])
    setStreaming(true)
    setTool('')
    try {
      await chat(text, threadId, (ev) => {
        if (ev.type === 'token' && ev.text) {
          setMessages((m) => {
            const copy = [...m]
            copy[copy.length - 1] = { role: 'agent', text: copy[copy.length - 1].text + ev.text }
            return copy
          })
        } else if (ev.type === 'tool') {
          setTool(ev.name || '')
        } else if (ev.type === 'error') {
          setErr(ev.message || 'agent error')
        }
      })
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setStreaming(false)
      setTool('')
    }
  }

  const newChat = () => {
    setMessages([])
    setThreadId(newThreadId())
    setTool('')
    setErr('')
  }

  return (
    <Modal
      open
      passiveModal
      modalHeading={title}
      onRequestClose={onClose}
      size="lg"
    >
      {subtitle && (
        <p
          style={{
            color: 'var(--cds-text-secondary)',
            fontSize: '0.9rem',
            marginTop: '-0.5rem',
            marginBottom: '0.75rem',
            paddingBottom: '0.5rem',
            borderBottom: '1px solid var(--cds-border-subtle)',
          }}
        >
          {subtitle}
        </p>
      )}
      {greeting && (
        <p style={{ color: 'var(--cds-text-secondary)', marginBottom: '0.75rem' }}>{greeting}</p>
      )}
      <div
        style={{
          height: '24rem',
          overflowY: 'auto',
          border: '1px solid var(--cds-border-subtle)',
          padding: '0.75rem',
          background: 'var(--cds-layer)',
        }}
      >
        {messages.length === 0 && (
          <p style={{ color: 'var(--cds-text-secondary)' }}>Say hello to start the conversation.</p>
        )}
        {messages.map((m, i) => (
          <div key={i} style={{ margin: '0.5rem 0', textAlign: m.role === 'user' ? 'right' : 'left' }}>
            <span
              style={{
                display: 'inline-block',
                maxWidth: '80%',
                padding: '0.5rem 0.75rem',
                borderRadius: '0.5rem',
                whiteSpace: m.role === 'user' ? 'pre-wrap' : 'normal',
                textAlign: 'left',
                background: m.role === 'user' ? 'var(--cds-button-primary)' : 'var(--cds-layer-accent)',
                color: m.role === 'user' ? 'var(--cds-text-on-color)' : 'var(--cds-text-primary)',
              }}
            >
              {m.role === 'agent' ? (
                m.text ? (
                  <Markdown text={m.text} />
                ) : (
                  streaming && i === messages.length - 1 ? '…' : ''
                )
              ) : (
                m.text
              )}
            </span>
          </div>
        ))}
        {tool && (
          <div style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem', fontStyle: 'italic' }}>
            running tool: {tool}
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {err && <p style={{ color: 'var(--cds-text-error)', margin: '0.5rem 0' }}>{err}</p>}

      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginTop: '0.75rem' }}>
        <TextInput
          id="agent-chat-input"
          labelText=""
          placeholder="Type a message…"
          value={input}
          disabled={streaming}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') send()
          }}
        />
        <Button renderIcon={SendAlt} disabled={streaming || !input.trim()} onClick={send}>
          Send
        </Button>
        <Button kind="ghost" disabled={streaming} onClick={newChat}>
          New chat
        </Button>
      </div>
    </Modal>
  )
}
