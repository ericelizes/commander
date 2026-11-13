// A cited source, shown as a popup to read. The daemon sends a board step or a
// status as JSON, and a Manager's report as markdown; sourcePage turns any of
// them into the same plain-language page.

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Markdown } from '../../../features/chat/Markdown'
import { plainTitle } from '../viewmodel'
import { failedDetail } from './GateChecklist'

export type Evidence = { id: string; label: string; text: string; at?: string }
export type Tone = 'you' | 'stuck' | 'agent' | 'waiting' | 'quiet' | 'done'

type Gate = { name: string; verifier?: string; status?: string; evidence?: string; by?: string; at?: number }
type Record = {
  title?: string; state?: string; status?: string; note?: string; waiting?: string; working?: boolean | number
  owner_state?: string; owner_why?: string; review?: { text?: string; acted?: boolean; no_pr?: boolean } | null; next_after?: string[] | null; gates?: Gate[] | null
  kind?: string; text?: string; why?: string
}

export type SourcePage = {
  title: string
  caveat?: string
  status?: { text: string; tone: Tone }
  lead?: string
  body?: string
  checks: { name: string; detail: string; passed: boolean; failed: boolean }[]
  after: string[]
}

const optional = (v: unknown, type: string) => v === undefined || typeof v === type
const isObject = (v: unknown): v is { [key: string]: unknown } => typeof v === 'object' && v !== null && !Array.isArray(v)

function isGate(g: unknown): g is Gate {
  return isObject(g) && typeof g.name === 'string' && ['verifier', 'status', 'evidence', 'by'].every((k) => optional(g[k], 'string')) && optional(g.at, 'number')
}

function isRecord(r: unknown): r is Record {
  if (!isObject(r) || (typeof r.kind !== 'string' && typeof r.status !== 'string')) return false
  return ['title', 'state', 'status', 'note', 'waiting', 'owner_state', 'owner_why', 'kind', 'text', 'why'].every((k) => optional(r[k], 'string'))
    && (optional(r.working, 'boolean') || typeof r.working === 'number')
    && (r.review == null || (isObject(r.review) && optional(r.review.text, 'string')))
    && (r.next_after == null || (Array.isArray(r.next_after) && r.next_after.every((id) => typeof id === 'string')))
    && (r.gates == null || (Array.isArray(r.gates) && r.gates.every(isGate)))
}

// The daemon numbers a Manager's message chat:<seq>. That is prose whatever it
// looks like; anything else is a record only if it has a record's exact shape.
function parseRecord(e: Evidence): Record | null {
  if (e.id.startsWith('chat:')) return null
  try {
    const r: unknown = JSON.parse(e.text)
    return isRecord(r) ? r : null
  } catch {
    return null
  }
}

const isAgentRole = (who: string) => /^(operator|manager|reviewer)-/.test(who)

// In the order and the words of the board's leafLine (livedata/workflow.go).
function stepStatus(r: Record): SourcePage['status'] {
  const waiting = r.waiting?.trim() ?? ''
  const gone = r.owner_state === 'died' || r.owner_state === 'finished'
  if (r.state === 'done' || r.status === 'closed') return { text: 'Done', tone: 'done' }
  if (r.status === 'deferred') return { text: 'Parked', tone: 'quiet' }
  if (!gone && (r.working || r.owner_state === 'working')) return { text: 'An agent is working on this', tone: 'agent' }
  if (r.state === 'needs-user' || waiting === 'you') return { text: 'Needs you', tone: 'you' }
  if (r.state === 'review' || waiting.split(' · ').includes('Manager to close')) return { text: 'Result ready for Manager review', tone: 'waiting' }
  if (r.owner_state === 'died') return { text: "Agent stopped: the Manager's move", tone: 'you' }
  if (r.owner_state === 'finished') return { text: "Agent finished, step not closed: the Manager's move", tone: 'you' }
  if (r.review?.acted) return { text: `${r.review.text}: the Manager's move`, tone: 'you' }
  if (r.review?.no_pr) return { text: 'Waiting for a review of no PR: nothing can clear it', tone: 'you' }
  if (r.review?.text) return { text: `Waiting for review: ${r.review.text}`, tone: 'waiting' }
  if (waiting) return { text: isAgentRole(waiting) ? 'Waiting on an agent' : `Waiting for ${waiting}`, tone: 'waiting' }
  if (r.state === 'blocked') return { text: 'Blocked', tone: 'waiting' }
  if (r.state === 'not-started') return { text: 'Not started', tone: 'quiet' }
  if (r.state === 'in-progress' || r.status === 'in_progress') return { text: 'In progress', tone: 'agent' }
  return r.status ? { text: 'Open', tone: 'quiet' } : undefined
}

const whoChecks = (who?: string) => who === 'user' ? 'you' : who === 'operator' || (who && isAgentRole(who)) ? 'an agent' : who

function checkDetail(g: Gate): string {
  const who = whoChecks(g.verifier)
  if (g.status === 'failed') {
    const when = [g.by && `checked by ${whoChecks(g.by)}`, g.at && formatWhen(g.at * 1000)].filter(Boolean).join(', ')
    return failedDetail(g).replace(/^f/, 'F') + (when ? ` (${when})` : '')
  }
  if (g.status === 'passed') return who ? `Passed, checked by ${who}` : 'Passed'
  return who ? `Waiting on ${who}` : 'Waiting'
}

const sentenceCase = (name: string) => {
  const words = name.replace(/[-_]+/g, ' ').trim()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

// titles names the steps a record refers to by id; those it can't name are counted.
export function sourcePage(e: Evidence, titles: Map<string, string>): SourcePage {
  const r = parseRecord(e)
  if (!r) {
    const m = /^(.*?)\s*\((.+)\)$/.exec(e.label)
    return { title: m ? m[1] : e.label, caveat: m ? sentenceCase(m[2]) : undefined, body: e.text, checks: [], after: [] }
  }
  if (r.kind !== undefined) return { title: e.label, lead: r.text, body: r.why || undefined, checks: [], after: [] }
  const title = plainTitle(e.label)
  const recorded = r.title && plainTitle(r.title)
  const status = stepStatus(r)
  const review = r.review?.text && !status?.text.includes(r.review.text) ? r.review.text : undefined
  const blockers = r.next_after ?? []
  const named = blockers.flatMap((id) => { const t = titles.get(id); return t ? [plainTitle(t)] : [] })
  const unnamed = blockers.length - named.length
  return {
    title,
    status,
    lead: recorded && recorded !== title ? recorded : undefined,
    body: [r.note, r.owner_why, review].filter(Boolean).join('\n\n') || undefined,
    checks: (r.gates ?? []).map((g) => ({ name: sentenceCase(g.name), detail: checkDetail(g), passed: g.status === 'passed', failed: g.status === 'failed' })),
    after: unnamed ? [...named, `${unnamed} ${named.length ? 'more ' : ''}step${unnamed === 1 ? '' : 's'} not named here`] : named,
  }
}

function formatWhen(at: string | number): string {
  const d = new Date(at)
  return Number.isNaN(d.getTime()) ? String(at) : d.toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

// SourceReader is a cited source as a popup at its number. Hovering shows it and
// it stays while the pointer is over it, so a long source can be scrolled; pinned
// (a click on the number) it stays until closed.
export function SourceReader({ source, titles, onClose, anchor, pinned, onEnter, onLeave }: {
  source: Evidence
  titles: Map<string, string>
  onClose: () => void
  anchor?: HTMLElement | null
  pinned?: boolean
  onEnter?: () => void
  onLeave?: () => void
}) {
  const ref = useRef<HTMLElement>(null)
  const [at, setAt] = useState<{ top: number; left: number } | null>(null)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el || !anchor) return
    const a = anchor.getBoundingClientRect()
    const box = el.getBoundingClientRect()
    const pane = el.closest('.ov-pane')?.getBoundingClientRect() ?? { left: 0, right: window.innerWidth }
    const left = Math.max(pane.left + 12, Math.min(a.left + a.width / 2 - box.width / 2, pane.right - box.width - 12))
    const below = a.bottom + 8
    setAt({ left, top: below + box.height <= window.innerHeight - 12 ? below : Math.max(12, a.top - box.height - 8) })
  }, [anchor, source.id])
  useEffect(() => {
    // Capture: a layer's own listener may already have removed it by bubble time.
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !document.querySelector('[aria-modal="true"], .health-pop')) onClose() }
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!ref.current?.contains(t) && !anchor?.contains(t)) onClose()
    }
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('mousedown', onDown)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('mousedown', onDown)
    }
  }, [onClose, anchor])
  const page = sourcePage(source, titles)
  const meta = page.caveat || source.at
  return (
    <aside ref={ref} className="ov-reader" role="dialog" aria-label="Supporting evidence" tabIndex={-1} onMouseEnter={onEnter} onMouseLeave={onLeave} style={anchor ? (at ?? { visibility: 'hidden' }) : undefined}>
      {pinned && (
        <button type="button" className="ov-reader-close" aria-label="Close source" onClick={onClose}>
          <svg width="14" height="14" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden><path d="M4 4l8 8M12 4l-8 8" /></svg>
        </button>
      )}
      <h2>{page.title}</h2>
      {meta && <p className="ov-reader-meta">{page.caveat}{page.caveat && source.at ? ', ' : ''}{source.at && <time dateTime={source.at}>{formatWhen(source.at)}</time>}</p>}
      {page.status && <p className="ov-reader-status" data-tone={page.status.tone}><span className="ov-dot" data-tone={page.status.tone} />{page.status.text}</p>}
      {page.lead && <p className="ov-reader-lead">{page.lead}</p>}
      {page.body && <div className="ov-reader-body"><Markdown text={page.body} /></div>}
      {page.checks.length > 0 && <section aria-label="Checks"><h3>Checks</h3><ul className="ov-reader-checks">{page.checks.map((c) => <li key={c.name} className={c.passed ? 'is-passed' : c.failed ? 'is-failed' : ''}><strong>{c.name}</strong><span>{c.detail}</span></li>)}</ul></section>}
      {page.after.length > 0 && <section aria-label="Comes after"><h3>Comes after</h3><ul className="ov-reader-after">{page.after.map((t) => <li key={t}>{t}</li>)}</ul></section>}
    </aside>
  )
}
