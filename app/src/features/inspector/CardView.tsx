import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

import { parseAnsi } from './ansi'
import { ago } from './model'
import type { Card } from './types'

type Props = {
  card: Card | null
  loading?: boolean
  error?: string | null
  onOpen: (url: string) => void
  now?: number
}

function lineClass(line: string): string {
  if (line.startsWith('✓')) return 'insp-line insp-ok'
  if (line.startsWith('✗')) return 'insp-line insp-bad'
  if (line.startsWith('⚑') || line.startsWith('○')) return 'insp-line insp-warn'
  return 'insp-line'
}

export function Ansi({ text }: { text: string }) {
  return (
    <pre className="insp-pre">
      {parseAnsi(text).map((s, i) => (
        <span key={i} style={{ color: s.fg, fontWeight: s.bold ? 700 : undefined, opacity: s.dim ? 0.7 : undefined }}>
          {s.text}
        </span>
      ))}
    </pre>
  )
}

// CardView draws an inspector.Card: header, CHECKS/REVIEW-style sections and
// the body (markdown, or raw command output).
export function CardView({ card, loading, error, onOpen, now }: Props) {
  if (!card) {
    return <div className="insp-card insp-muted">{error ? <span className="insp-bad">{error}</span> : loading ? 'Fetching…' : ''}</div>
  }
  const fetched = ago(card.FetchedAt, now)
  return (
    <div className="insp-card">
      <div className="insp-card-head">
        <div className="insp-card-title">
          {card.State && <span className={`insp-badge insp-state-${card.State.toLowerCase()}`}>{card.State}</span>}
          <span>{card.Title}</span>
        </div>
        {(card.Meta?.length || fetched) && (
          <div className="insp-card-meta">
            {card.Meta?.map((m, i) => <span key={i}>{m}</span>)}
            {fetched && <span className="insp-muted">{loading ? 'refreshing…' : fetched}</span>}
          </div>
        )}
        {card.URL && (
          <button className="insp-linkbtn" onClick={() => onOpen(card.URL)} title={card.URL}>
            {card.URL}
          </button>
        )}
      </div>
      {card.Err && <div className="insp-err">{card.Err}</div>}
      {card.Sections?.map((s) => (
        <div className="insp-section" key={s.Title}>
          <div className="insp-section-title">{s.Title}</div>
          {s.Lines?.map((l, i) => (
            <div key={i} className={lineClass(l)}>
              {l}
            </div>
          ))}
        </div>
      ))}
      {card.Markdown &&
        (card.Preformatted ? (
          <Ansi text={card.Markdown} />
        ) : (
          <div className="insp-md">
            <Markdown
              remarkPlugins={[remarkGfm]}
              components={{
                a: ({ href, children }) => (
                  <a
                    href={href}
                    onClick={(e) => {
                      e.preventDefault()
                      if (href) onOpen(href)
                    }}
                  >
                    {children}
                  </a>
                ),
              }}
            >
              {card.Markdown}
            </Markdown>
          </div>
        ))}
    </div>
  )
}
