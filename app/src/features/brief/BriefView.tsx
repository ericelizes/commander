// BriefView is the Brief page: one document in three parts (Moving today, Done
// yesterday, This week), each a short paragraph and bullets, written by an
// agent that read the records. What a sentence cites shows on hover only.

import { Fragment } from 'react'
import { useStore } from '../../renderer/src/store'
import { useIntegrations } from '../linear/settings'
import { regenerateBrief, useBrief, useBriefStatus, type BriefEvidence, type BriefSentence } from './brief'

function when(iso: string | undefined, zone: string) {
  if (!iso) return ''
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  try {
    return at.toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit', ...(zone ? { timeZone: zone } : {}) })
  } catch {
    return at.toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' })
  }
}

// inline shows **bold** as bold; everything else is plain text.
function inline(text: string) {
  return text.split(/(\*\*[^*]+\*\*)/).map((part, i) => (part.startsWith('**') && part.endsWith('**') && part.length > 4 ? <strong key={i}>{part.slice(2, -2)}</strong> : <Fragment key={i}>{part}</Fragment>))
}

// cites is a sentence's hover: what each source is.
function cites(s: BriefSentence, evidence: Map<string, BriefEvidence>) {
  return s.sources.map((id) => evidence.get(id)?.label ?? id).join('\n')
}

const windows = ['today', 'yesterday', 'week'] as const

export function BriefView() {
  useBriefStatus()
  const config = useStore((s) => s.config)
  const zone = useIntegrations((x) => x.timezone)
  const { status, failed } = useBrief()
  const brief = status?.brief
  const evidence = new Map((status?.evidence ?? []).map((e) => [e.id, e]))
  const generating = status?.generating ?? false
  return (
    <div className="progress-page">
      <div className="progress-col brief-col">
        <h1 className="progress-title">Brief</h1>
        <div className="brief-meta">
          <span className="sp-dates">
            {brief ? `Written ${when(brief.generated_at, zone)}` : status ? 'Not written yet' : ''}
            {status?.next_at && ` · next ${when(status.next_at, zone)}`}
          </span>
          <button type="button" className="btn" disabled={!config || generating} onClick={() => config && void regenerateBrief(config)}>
            {generating ? 'Writing…' : 'Regenerate'}
          </button>
        </div>
        {failed && <p role="alert" className="wf-note">The Brief could not be loaded.</p>}
        {status?.error && <p role="alert" className="wf-note">{status.error}</p>}
        {!status && !failed && <p className="wf-note">Loading…</p>}
        {status && !brief && !generating && <p className="wf-note">cmdrd writes the Brief at 7 AM, 1 PM, 7 PM and 1 AM Eastern. Regenerate writes one now.</p>}
        {brief && (
          <article className="brief-doc">
            {brief.sections.map((s, i) => (
              <section key={s.title} aria-label={s.title}>
                <h2>
                  {s.title}
                  {windows[i] && <span className="brief-sub">{brief.windows[windows[i]]?.label}</span>}
                </h2>
                {s.paragraph.length > 0 && (
                  <p>
                    {s.paragraph.map((x, n) => (
                      <Fragment key={n}>
                        {n > 0 && ' '}
                        <span title={cites(x, evidence)}>{inline(x.text)}</span>
                      </Fragment>
                    ))}
                  </p>
                )}
                {s.bullets.length > 0 && (
                  <ul>
                    {s.bullets.map((b, n) => <li key={n} title={cites(b, evidence)}>{inline(b.text)}</li>)}
                  </ul>
                )}
                {!s.paragraph.length && !s.bullets.length && <p className="brief-none">Nothing to report.</p>}
              </section>
            ))}
          </article>
        )}
        {brief?.linear_note && <p className="wf-note">Linear tickets are not included: {brief.linear_note}.</p>}
      </div>
    </div>
  )
}
