import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { BoardRow, BoardTrack, LineKind, SnapStep, SnapWorkstream, Snapshot } from '../../../shared/snapshot'
import { splitTickets, TicketPills } from '../../../features/linear/Ticket'
import { useStore } from '../store'
import { askKey, findTrack, openNested, plainTitle } from '../viewmodel'
import { diagramFor } from './DerivedDiagram'
import { SourceReader, type Evidence, type Tone } from './OverviewSource'
import './overview.css'

type Sentence = { text: string; sources: string[] }
// A daemon from before the per-sentence recap sends one block with its sources.
type Claim = { sentences: Sentence[] } | Sentence
const sentencesOf = (claim: Claim): Sentence[] => ('sentences' in claim ? claim.sentences : [claim])
export type RecapResult = {
  state: 'waiting' | 'updating' | 'ready' | 'unavailable'
  stale: boolean
  error?: string
  evidence: Evidence[]
  recap?: { summary: Claim; changes: Claim[]; next: Claim[]; generated_at: string }
}

export function scopedSteps(ws: SnapWorkstream, track?: string): SnapStep[] {
  if (!track) return ws.steps ?? []
  const find = (list: SnapStep[]): SnapStep | undefined => {
    for (const s of list) { if (s.bead_id === track && s.track) return s; const child = find(s.children ?? []); if (child) return child }
  }
  const found = find(ws.steps ?? [])
  return found ? [found] : []
}

export function overviewAsks(snapshot: Snapshot, ws: SnapWorkstream, track?: string) {
  const ids = new Set<string>()
  const visit = (steps: SnapStep[]) => { for (const s of steps) { ids.add(s.bead_id); visit(s.children ?? []) } }
  visit(scopedSteps(ws, track))
  const uniqueTitle = snapshot.workstreams.filter((w) => w.project === ws.project && w.title === ws.title).length === 1
  return snapshot.needs_you.filter((a) => a.project === ws.project && (ids.has(a.bead_id) || (!track && a.bead_id === ws.bead_id) || (uniqueTitle && a.workstream === ws.title && (!track || a.track_id === track))))
}

// overviewTickets is every ticket the scope carries, in a title or a ticket:
// label; a workstream also counts its open tracks', as Commander's check does.
export function overviewTickets(ws: SnapWorkstream, track?: string): string[] {
  const scope: { title: string; tickets?: string[] }[] = track ? scopedSteps(ws, track) : [ws, ...openNested(ws)]
  return [...new Set(scope.flatMap((s) => [...splitTickets(s.title).tickets, ...(s.tickets ?? [])]))]
}

function toneOf(kind?: LineKind): Tone {
  if (kind === 'needs' || kind === 'stuck' || kind === 'stalled') return 'you'
  if (kind === 'working') return 'agent'
  if (kind === 'waiting') return 'waiting'
  return 'quiet'
}

export function Overview({ ws, row, track, onWorkflow, onTrack }: {
  ws: SnapWorkstream; row: BoardRow; track?: string; onWorkflow: () => void; onTrack: (id: string) => void
}) {
  const snapshot = useStore((s) => s.snapshot)!
  const cfg = useStore((s) => s.config)
  const conn = useStore((s) => s.conn)
  const [result, setResult] = useState<RecapResult | null>(null)
  const [error, setError] = useState('')
  const [source, setSource] = useState<{ evidence: Evidence; anchor: HTMLElement; pinned: boolean } | null>(null)
  // Hover is forgiving: a brush past a number shows nothing, and the pointer can
  // cross the gap from the number to the popup without losing it.
  const hoverTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const cancelHover = useCallback(() => clearTimeout(hoverTimer.current), [])
  const closeSource = useCallback(() => {
    clearTimeout(hoverTimer.current)
    setSource(null)
  }, [])
  const hoverOpen = (evidence: Evidence, anchor: HTMLElement) => {
    clearTimeout(hoverTimer.current)
    hoverTimer.current = setTimeout(() => setSource((s) => (s?.pinned ? s : { evidence, anchor, pinned: false })), 120)
  }
  const hoverClose = useCallback(() => {
    clearTimeout(hoverTimer.current)
    hoverTimer.current = setTimeout(() => setSource((s) => (s?.pinned ? s : null)), 200)
  }, [])
  useEffect(() => closeSource, [closeSource])
  useEffect(() => closeSource(), [closeSource, ws.project, ws.bead_id, track])
  const openAsk = useStore((s) => (s.selected.kind === 'ask' ? s.selected.key : ''))
  const trackStep = track ? scopedSteps(ws, track)[0] : undefined
  const trackDiagram = track ? diagramFor(row, track) : null
  const current = trackDiagram?.steps.find((s) => s.id === trackDiagram.current)
  const nestedRow = findTrack(row, track)
  const below = !track || track === row.scope ? row.tracks : nestedRow?.children ?? []
  const target: BoardTrack | BoardRow = nestedRow ?? (track === row.scope ? row : trackStep ? {
    ...row, title: trackStep.title,
    text: current?.text ?? (trackStep.status === 'closed' ? 'Completed' : 'No current status is available.'),
    working: trackStep.working ? 1 : 0,
    kind: current?.kind === 'done' ? 'quiet' : current?.kind ?? 'idle',
  } : row)
  const tickets = useMemo(() => overviewTickets(ws, track), [ws, track])
  const asks = overviewAsks(snapshot, ws, track)
  const degraded = !!snapshot.degraded || conn !== 'live'
  const steps = useMemo(() => scopedSteps(ws, track), [ws, track])
  const artifacts = useMemo(() => {
    const prs = new Set<string>()
    const visit = (list: SnapStep[]) => { for (const s of list) { for (const p of s.pr ?? []) if (/^\d+$/.test(p)) prs.add(p); if (s.review && /^\d+$/.test(s.review)) prs.add(s.review); visit(s.children ?? []) } }
    visit(steps)
    return [...prs]
  }, [steps])

  const stepTitles = useMemo(() => {
    const titles = new Map((result?.evidence ?? []).map((e) => [e.id, e.label]))
    const visit = (list: SnapStep[]) => { for (const s of list) { titles.set(s.bead_id, s.title); visit(s.children ?? []) } }
    visit(ws.steps ?? [])
    return titles
  }, [result, ws.steps])

  useEffect(() => {
    if (!cfg || degraded) return
    let cancelled = false
    let controller: AbortController | null = null
    let running = false
    const load = async () => {
      if (running) return
      running = true; controller = new AbortController()
      const timer = setTimeout(() => controller?.abort(), 12_000)
      try {
        const r = await fetch(`${cfg.apiUrl}/api/overview`, {
          method: 'POST', headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
          body: JSON.stringify({ project: ws.project, bead_id: ws.bead_id, track_id: track ?? '' }), signal: controller.signal,
        })
        if (!r.ok) throw new Error('Recap unavailable')
        const body = await r.json() as RecapResult
        if (!['waiting', 'updating', 'ready', 'unavailable'].includes(body.state) || !Array.isArray(body.evidence)) throw new Error('Invalid recap')
        if (!cancelled) { setResult(body); setError('') }
      } catch {
        if (!cancelled) setError('The recap is unavailable. Live status and decisions are shown below.')
      } finally { clearTimeout(timer); running = false }
    }
    void load()
    const timer = setInterval(() => void load(), 15_000)
    return () => { cancelled = true; clearInterval(timer); controller?.abort() }
  }, [cfg, ws.project, ws.bead_id, track, degraded])

  // One number per source across the whole recap, so a source cited twice reads
  // as the same footnote.
  const sourceNumbers = useMemo(() => {
    const numbers = new Map<string, number>()
    const recap = result?.recap
    if (recap) for (const c of [recap.summary, ...recap.changes, ...recap.next]) for (const s of sentencesOf(c)) for (const id of s.sources) if (!numbers.has(id)) numbers.set(id, numbers.size + 1)
    return numbers
  }, [result])
  // The marks close their own sentence, held to its last word so they never sit alone on a line.
  const cite = (claim: Claim) => sentencesOf(claim).map((s, n) => {
    const lastWord = s.text.lastIndexOf(' ') + 1
    return (
      <Fragment key={n}>
        {n > 0 && ' '}
        {s.text.slice(0, lastWord)}
        <span className="ov-cited">
          {s.text.slice(lastWord)}
          <span className="ov-cites">{s.sources.map((id) => {
            const e = result?.evidence.find((x) => x.id === id)
            return e ? <button key={id} type="button" className={source?.evidence.id === id ? 'is-open' : ''} aria-label={`Evidence: ${e.label}`}
              onMouseEnter={(ev) => hoverOpen(e, ev.currentTarget)} onMouseLeave={hoverClose}
              onClick={(ev) => { cancelHover(); const anchor = ev.currentTarget; setSource((s) => (s?.pinned && s.evidence.id === id ? null : { evidence: e, anchor, pinned: true })) }}>{sourceNumbers.get(id)}</button> : null
          })}</span>
        </span>
      </Fragment>
    )
  })
  const stalled = target.kind === 'stalled' || target.kind === 'stuck'
  const pendingDecision = asks.length === 0 && (target.kind === 'needs' || (!track && row.flag > 0))
  const noAsk = pendingDecision ? 'A decision is recorded, but its details are not available yet.' : 'No decision is requested from you. Agent recovery is needed.'

  return (
    <div className="ov-pane">
    <div className="ov-scroll" onScroll={() => setSource((s) => (s?.pinned ? s : null))}>
      <article className="ov" aria-label="Workstream overview">
        <h1>{splitTickets(target.title).text}</h1>
        {tickets.length > 0 && <div className="ov-tickets"><TicketPills tickets={tickets} progress={target.milestones ?? target.progress} /></div>}
        {asks.length === 0 && <p className="ov-live"><span className="ov-dot" data-tone={toneOf(target.kind)} />{target.text || 'No current activity has been recorded.'}</p>}
        {target.why && <p className="ov-warning">{target.why}</p>}
        {degraded && <p role="status" className="ov-warning">Connection interrupted. This status may be out of date.</p>}
        {(asks.length > 0 || pendingDecision || stalled) && <section className={`ov-attention${asks.length ? ' ov-needs' : ''}${stalled ? ' ov-stalled' : ''}`} aria-label="Needs you">
          <h2>{asks.length ? 'Needs you' : pendingDecision ? 'Decision details pending' : 'Recovery needed'}</h2>
          {asks.length ? asks.map((ask) => (
            <div className="ov-ask" key={ask.bead_id}>
              <h3>{ask.decision?.question || ask.ask || ask.title}</h3>
              {ask.decision?.context && <p>{decisionPreview(ask.decision.context)}</p>}
              {ask.stale_why && <p className="ov-warning">This request needs a fresh check: {ask.stale_why}</p>}
              {openAsk !== askKey(ask) && <button type="button" className="ov-action" onClick={() => useStore.getState().select({ kind: 'ask', key: askKey(ask) })}>Answer this</button>}
            </div>
          )) : <p>{noAsk}</p>}
        </section>}
        <section aria-label="Recap">
          {result?.recap ? <div>
            {(result.stale || degraded) && <p className="ov-warning">Earlier recap. Current status and decisions above take precedence.</p>}
            <p className="ov-summary">{cite(result.recap.summary)}</p>
            {result.recap.changes.length > 0 && <><h2>What changed</h2><ul>{result.recap.changes.map((c, i) => <li key={i}>{cite(c)}</li>)}</ul></>}
            {result.recap.next.length > 0 && <><h2>What happens next</h2><ul>{result.recap.next.map((c, i) => <li key={i}>{cite(c)}</li>)}</ul></>}
          </div> : <p className="ov-muted" role="status">{error || result?.error || (degraded ? 'Recap updates will resume when live state is available.' : 'Reading the latest evidence for a recap…')}</p>}
          {result?.recap && (error || result.error) && <p className="ov-warning">{error || result.error}</p>}
          {result?.state === 'updating' && result.recap && <p className="ov-muted" role="status">Updating the recap from newer evidence…</p>}
        </section>
        {below.length > 0 && <section aria-label="Workstreams under this one"><h2>Across this workstream</h2><div className="ov-tracks">{below.map((t) => <button type="button" key={t.bead_id} onClick={() => onTrack(t.bead_id)}><strong>{plainTitle(t.title)}</strong><span data-tone={toneOf(t.kind)}>{t.text}</span></button>)}</div></section>}
        <footer className="ov-footer">
          {ws.repo && /^[\w.-]+\/[\w.-]+$/.test(ws.repo) && artifacts.map((p) => <a key={p} href={`https://github.com/${ws.repo}/pull/${p}`} target="_blank" rel="noreferrer">PR #{p} ↗</a>)}
          <button type="button" onClick={onWorkflow}>View workflow</button>
          <span>{result?.recap && `Recap written ${new Date(result.recap.generated_at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}. `}State recorded {new Date(snapshot.generated_at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}.</span>
        </footer>
      </article>
    </div>
    {source && <SourceReader source={source.evidence} anchor={source.anchor} pinned={source.pinned} titles={stepTitles} onClose={closeSource} onEnter={cancelHover} onLeave={hoverClose} />}
    </div>
  )
}

function decisionPreview(text: string): string {
 const plain = text.replace(/```[\s\S]*?```/g, '').replace(/^#{1,6}\s+.*$/gm, '').replace(/\*\*/g, '').trim()
 const paragraph = plain.split(/\n\s*\n/).find((p) => p.trim().length > 30) ?? plain
 return paragraph.length > 600 ? `${paragraph.slice(0, 600).trimEnd()}…` : paragraph
}
