// HealthBadge is the one place problems show: a badge in the rail
// while something is not working (cmdrd cannot read its state, a health
// problem keeps failing, a workflow error) or the screen disagrees with what is
// happening or a merge waits on its review (red for the first, amber for the
// second). Clicking it opens the list; an item with a workstream opens it. It
// clears on its own when the problems do.

import { useEffect, useRef, useState } from 'react'
import { useStore } from '../store'
import { workflowFindings } from '../workflowFindings'

export function HealthBadge() {
  const snapshot = useStore((s) => s.snapshot)
  const findings = workflowFindings(snapshot)
  const errors = findings.filter(f => f.kind === 'error')
  const blockers = findings.filter(f => f.kind === 'blocker')
  const advisories = findings.filter(f => f.kind === 'advisory')
  const degraded = useStore((s) => s.snapshot?.degraded)
  const health = useStore((s) => s.snapshot?.health)
  const wrong = useStore((s) => s.snapshot?.wrong)
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLSpanElement>(null)
  const items = [
    ...(degraded ? [{ key: 'degraded', head: 'Commander cannot read its state: everything shown may be stale', lines: [] as string[], detail: degraded.reason, since: 0 }] : []),
    ...(health ?? []).filter(h => !snapshot?.display || !h.key.startsWith('plan:')).map((h) => ({ key: h.key, head: h.detail.split('\n')[0].replace(/:$/, ''), lines: ruleLines(h.detail), detail: h.detail, since: h.since })),
  ]
  const broken = items.length
  const mismatches = wrong ?? []
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', esc)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', esc)
    }
  }, [open])
  if (broken + mismatches.length + errors.length + blockers.length + advisories.length === 0) return null
  const parts = [broken && `${broken} not working`, errors.length && count(errors.length, 'error', 'errors'), mismatches.length && `${mismatches.length} wrong on screen`, blockers.length && count(blockers.length, 'blocker', 'blockers'), advisories.length && count(advisories.length, 'advisory', 'advisories')].filter(Boolean)
  const label = parts.join(', ')
  return (
    <span className="health" ref={ref}>
      <button type="button" className={`rail-dot health-badge${broken + errors.length ? '' : ' only-wrong'}${open ? ' on' : ''}`} onClick={() => setOpen(!open)} title={label} aria-label={label} aria-expanded={open}>
        <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden>
          <path d="M8 3.2v5.8" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" />
          <circle cx="8" cy="12.6" r="1.3" fill="currentColor" />
        </svg>
      </button>
      {open && (
        <div className="health-pop" role="dialog" aria-label={label}>
          {broken > 0 && <p className="health-pop-head">{broken} not working</p>}
          <ul>
            {items.map((i) => (
              <li key={i.key} title={i.detail}>
                <span className="health-pop-what">{i.head}</span>
                {i.lines.length > 0 && (
                  <ul className="health-pop-lines">
                    {i.lines.map((l, k) => (
                      <li key={k}>{l}</li>
                    ))}
                  </ul>
                )}
                {i.since > 0 && <span className="health-pop-since">since {new Date(i.since * 1000).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}</span>}
              </li>
            ))}
          </ul>
          {[{ title: 'Workflow errors', rows: errors }, { title: 'Blockers', rows: blockers }, { title: 'Advisories', rows: advisories }].map(group => group.rows.length > 0 && (
            <div key={group.title}>
              <p className="health-pop-head">{group.title} ({group.rows.length})</p>
              <ul>{group.rows.map(f => <li key={f.key}><button type="button" className="health-pop-link" onClick={() => { setOpen(false); useStore.getState().openWorkflow(f.target) }}>{f.text}</button></li>)}</ul>
            </div>
          ))}
          {mismatches.length > 0 && (
            <>
              <p className="health-pop-head">Doesn't match what's happening</p>
              <ul>
                {mismatches.map((m) => (
                  <li key={m.key}>
                    {m.target ? (
                      <button
                        type="button"
                        className="health-pop-link"
                        onClick={() => {
                          setOpen(false)
                          useStore.getState().openWorkflow(m.target!)
                        }}
                      >
                        {m.text}
                      </button>
                    ) : (
                      <span className="health-pop-what">{m.text}</span>
                    )}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </span>
  )
}

const count = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`

// ruleLines are a plan problem's broken rules, each as the step and why (the
// fix commands are for its Manager): "- id \"Title\": why. Fix: ..." -> "Title: why".
function ruleLines(detail: string): string[] {
  return detail
    .split('\n')
    .filter((l) => l.startsWith('- '))
    .map((l) => {
      const m = /^- \S+ "(.*?)": (.*?)(?:\. Fix: .*)?$/.exec(l)
      return m ? `${m[1]}: ${m[2]}` : l.slice(2)
    })
}
