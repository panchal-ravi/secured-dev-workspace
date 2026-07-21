import { Tag } from '@carbon/react'

// The coding agent a workspace template ships. Empty/unknown is treated as Claude
// Code — the historical default before the IBM Bob Shell template existed.
type AgentMeta = { label: string; tagType: 'blue' | 'teal'; accent: string }

// agentMeta maps a flavor's coding_agent to its display identity. The accent colour
// (Carbon blue-60 / teal-60) is reused for the picker tile's left border so Claude
// Code and IBM Bob Shell templates read as visually distinct at a glance.
export function agentMeta(codingAgent?: string): AgentMeta {
  if (codingAgent === 'bob') {
    return { label: 'IBM Bob Shell', tagType: 'teal', accent: '#007d79' }
  }
  return { label: 'Claude Code', tagType: 'blue', accent: '#0f62fe' }
}

// AgentBadge is the prominent coding-agent chip shown on each template tile.
export default function AgentBadge({ codingAgent }: { codingAgent?: string }) {
  const m = agentMeta(codingAgent)
  return <Tag type={m.tagType}>{m.label}</Tag>
}
