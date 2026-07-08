import { Fragment, ReactNode } from 'react'

// Markdown renders the constrained subset of Markdown that LLM chat output uses:
// fenced code blocks, headings, ordered/unordered lists, and inline **bold**,
// *italic*, `code`, and [links](url). It is deliberately small — no dependency —
// and safe: every value goes through React's text nodes (auto-escaped), and link
// hrefs are restricted to http(s)/mailto so a model can't emit a javascript: URI.

const INLINE = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*]+\*)|(_[^_]+_)|(\[[^\]]+\]\([^)]+\))/g

function renderInline(text: string): ReactNode[] {
  const out: ReactNode[] = []
  let last = 0
  let m: RegExpExecArray | null
  INLINE.lastIndex = 0
  let key = 0
  while ((m = INLINE.exec(text)) !== null) {
    if (m.index > last) out.push(<Fragment key={key++}>{text.slice(last, m.index)}</Fragment>)
    const tok = m[0]
    if (tok.startsWith('`')) {
      out.push(
        <code key={key++} style={{ background: 'var(--cds-layer)', padding: '0.05rem 0.3rem', borderRadius: '0.2rem', fontSize: '0.85em' }}>
          {tok.slice(1, -1)}
        </code>,
      )
    } else if (tok.startsWith('**')) {
      out.push(<strong key={key++}>{tok.slice(2, -2)}</strong>)
    } else if (tok.startsWith('*') || tok.startsWith('_')) {
      out.push(<em key={key++}>{tok.slice(1, -1)}</em>)
    } else {
      // [text](url)
      const mm = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(tok)!
      const url = mm[2]
      const safe = /^(https?:|mailto:)/i.test(url)
      out.push(
        safe ? (
          <a key={key++} href={url} target="_blank" rel="noreferrer">
            {mm[1]}
          </a>
        ) : (
          <Fragment key={key++}>{mm[1]}</Fragment>
        ),
      )
    }
    last = m.index + tok.length
  }
  if (last < text.length) out.push(<Fragment key={key++}>{text.slice(last)}</Fragment>)
  return out
}

export default function Markdown({ text }: { text: string }) {
  const lines = text.split('\n')
  const blocks: ReactNode[] = []
  let i = 0
  let key = 0

  while (i < lines.length) {
    const line = lines[i]

    // Fenced code block.
    if (/^\s*```/.test(line)) {
      const body: string[] = []
      i++
      while (i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i++])
      i++ // closing fence
      blocks.push(
        <pre
          key={key++}
          style={{ background: 'var(--cds-layer)', padding: '0.6rem 0.75rem', borderRadius: '0.35rem', overflowX: 'auto', margin: '0.4rem 0', fontSize: '0.85em' }}
        >
          <code>{body.join('\n')}</code>
        </pre>,
      )
      continue
    }

    // GFM table: a `| … |` header row followed by a `|---|---|` separator row.
    if (
      /^\s*\|.*\|\s*$/.test(line) &&
      i + 1 < lines.length &&
      /^\s*\|?[\s:|-]*-[\s:|-]*\|?\s*$/.test(lines[i + 1]) &&
      lines[i + 1].includes('-')
    ) {
      const splitRow = (r: string) =>
        r.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map((c) => c.trim())
      const header = splitRow(line)
      i += 2 // header + separator
      const rows: string[][] = []
      while (i < lines.length && /^\s*\|.*\|\s*$/.test(lines[i])) rows.push(splitRow(lines[i++]))
      const cell = { border: '1px solid var(--cds-border-subtle)', padding: '0.3rem 0.55rem', textAlign: 'left' as const }
      blocks.push(
        <div key={key++} style={{ overflowX: 'auto', margin: '0.4rem 0' }}>
          <table style={{ borderCollapse: 'collapse', fontSize: '0.9em' }}>
            <thead>
              <tr>
                {header.map((c, n) => (
                  <th key={n} style={{ ...cell, background: 'var(--cds-layer)', fontWeight: 600 }}>
                    {renderInline(c)}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r, rn) => (
                <tr key={rn}>
                  {header.map((_, cn) => (
                    <td key={cn} style={cell}>
                      {renderInline(r[cn] ?? '')}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>,
      )
      continue
    }

    // Heading.
    const h = /^(#{1,6})\s+(.*)$/.exec(line)
    if (h) {
      const level = h[1].length
      blocks.push(
        <p key={key++} style={{ fontWeight: 600, fontSize: level <= 2 ? '1.05rem' : '1rem', margin: '0.5rem 0 0.25rem' }}>
          {renderInline(h[2])}
        </p>,
      )
      i++
      continue
    }

    // Unordered list.
    if (/^\s*[-*]\s+/.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*[-*]\s+/, ''))
      blocks.push(
        <ul key={key++} style={{ margin: '0.25rem 0', paddingLeft: '1.25rem', listStyle: 'disc' }}>
          {items.map((it, n) => (
            <li key={n}>{renderInline(it)}</li>
          ))}
        </ul>,
      )
      continue
    }

    // Ordered list.
    if (/^\s*\d+\.\s+/.test(line)) {
      const items: string[] = []
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) items.push(lines[i++].replace(/^\s*\d+\.\s+/, ''))
      blocks.push(
        <ol key={key++} style={{ margin: '0.25rem 0', paddingLeft: '1.5rem', listStyle: 'decimal' }}>
          {items.map((it, n) => (
            <li key={n}>{renderInline(it)}</li>
          ))}
        </ol>,
      )
      continue
    }

    // Blank line — skip (paragraph separator).
    if (line.trim() === '') {
      i++
      continue
    }

    // Paragraph: consecutive non-blank, non-structural lines joined with <br/>.
    const para: string[] = []
    while (
      i < lines.length &&
      lines[i].trim() !== '' &&
      !/^\s*```/.test(lines[i]) &&
      !/^(#{1,6})\s+/.test(lines[i]) &&
      !/^\s*[-*]\s+/.test(lines[i]) &&
      !/^\s*\d+\.\s+/.test(lines[i])
    ) {
      para.push(lines[i++])
    }
    blocks.push(
      <p key={key++} style={{ margin: '0.25rem 0' }}>
        {para.map((p, n) => (
          <Fragment key={n}>
            {n > 0 && <br />}
            {renderInline(p)}
          </Fragment>
        ))}
      </p>,
    )
  }

  return <>{blocks}</>
}
