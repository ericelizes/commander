// BriefView is the Brief page: what I'm moving today, what I finished on the
// previous workday and how this week's workstreams moved, every sentence
// written from cited records. Each sentence's sources show on hover.

import { useStore } from '../../renderer/src/store'
import { useIntegrations } from '../linear/settings'
import { regenerateBrief, useBrief, useBriefStatus, type BriefEvidence, type BriefGroup } from './brief'

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

function Section({ title, sub, groups, evidence, empty }: { title: string; sub?: string; groups: BriefGroup[] | null; evidence: Map<string, BriefEvidence>; empty: string }) {
  return (
    <section className="brief-section" aria-label={title}>
      <h2>
        {title}
        {sub && <span className="brief-sub">{sub}</span>}
      </h2>
      {!groups?.length && <p className="sp-hint">{empty}</p>}
      {groups?.map((g) => (
        <div key={g.workstream} className="brief-group">
          <div className="brief-ws">
            <span className="brief-ws-name">{g.name || 'Other'}</span>
            {g.progress != null && (
              <span className="brief-pct" title={`${g.progress}% done on the board`}>
                <span className="sp-bar"><span style={{ width: `${g.progress}%` }} /></span>
                <span className="brief-pct-n">{g.progress}%</span>
              </span>
            )}
          </div>
          {g.sentences.map((s, i) => (
            <p key={i} className="brief-line" title={s.sources.map((id) => evidence.get(id)?.label ?? id).join('\n')}>
              {s.text}
            </p>
          ))}
        </div>
      ))}
    </section>
  )
}

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
          <>
            <Section title="Moving today" sub={brief.windows.today.label} groups={brief.today} evidence={evidence} empty="Nothing is moving today." />
            <Section title="Done yesterday" sub={brief.windows.yesterday.label} groups={brief.yesterday} evidence={evidence} empty="Nothing was finished." />
            <Section title="This week" sub={brief.windows.week.label} groups={brief.week} evidence={evidence} empty="Nothing recorded this week." />
            {brief.linear_note && <p className="wf-note">Linear tickets are not included: {brief.linear_note}.</p>}
          </>
        )}
      </div>
    </div>
  )
}
