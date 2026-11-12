import { Component, Fragment, memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { BoardRow, BoardTrack, LineKind, SnapStep, SnapWorkstream, Snapshot } from '../../../shared/snapshot'
import { splitTickets, TicketPills } from '../../../features/linear/Ticket'
import { useSmoothText } from '../../../features/chat/thread/streaming'
import { useStore } from '../store'
import { askKey, findTrack, openNested, plainTitle } from '../viewmodel'
import { diagramFor } from './DerivedDiagram'
import { Spinner } from './Glyphs'
import { SourceReader, type Evidence, type Tone } from './OverviewSource'
import './overview.css'

// partial: a draft sentence still being written (no sources yet).
type Sentence = { text: string; sources: string[]; partial?: boolean }
// A daemon from before the per-sentence recap sends one block with its sources.
type Claim = { sentences: Sentence[] } | Sentence
const sentencesOf = (claim: Claim): Sentence[] => ('sentences' in claim ? claim.sentences ?? [] : [claim])
export type RecapResult = {
  // waiting/retrying are transient: the daemon says what it waits for and the tab asks again.
  state: 'waiting' | 'updating' | 'retrying' | 'ready' | 'unavailable'
  stale: boolean
  error?: string
  waiting_for?: string
  evidence: Evidence[]
  recap?: { summary: Claim; changes: Claim[]; next: Claim[]; generated_at: string }
  // What the reader has written so far while state is updating.
  draft?: { summary: Claim; changes?: Claim[] | null; next?: Claim[] | null }
}

// The recap's own wording of a wait, so a transient wait never reads as a failure.
export function waitText(r: RecapResult | null, degraded: boolean, reconnecting: boolean): string {
  if (degraded) return 'Recap updates will resume when live state is available.'
  if (reconnecting) return 'Waiting for Commander to answer. Trying again shortly…'
  if (!r) return 'Reading the latest evidence for a recap…'
  if (r.waiting_for) return `Waiting for ${r.waiting_for}. This tab retries by itself.`
  if (r.state === 'retrying') return 'The last try at writing the recap did not finish. Trying again shortly…'
  if (r.state === 'updating') return 'Writing the recap…'
  return 'Reading the latest evidence for a recap…'
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

// A word that has been drawn is never drawn again: only new words mount, and
// each fades in once (the chat's own fade).
const Word = memo(function Word({ word, fade }: { word: string; fade: boolean }) {
  return <span className={fade ? 'ct-fade-word' : undefined}>{word}</span>
})

// withCode shows `backticked` text as inline code.
function withCode(text: string): React.ReactNode {
  if (!text.includes('`')) return text
  return text.split(/(`[^`]+`)/g).map((part, i) =>
    part.length > 2 && part.startsWith('`') && part.endsWith('`') ? <code key={i} className="ov-code">{part.slice(1, -1)}</code> : part)
}

function Words({ text, settled }: { text: string; settled: number }) {
  let offset = 0
  return <>{(text.match(/\S+\s*|\s+/g) ?? []).map((w, i) => {
    offset += w.length
    return <Word key={i} word={w} fade={offset > settled} />
  })}</>
}

// StreamSentence paces a sentence's text the way the chat paces a reply. The
// sources attach once the sentence is complete and fully shown. A text that no
// longer extends the one shown here (a refresh's draft taking an earlier
// recap's place) starts over: while the draft is live it is paced and faded
// from its first character, never shown whole up to the old length.
export function StreamSentence({ s, done, live = false }: { s: Sentence; done: (s: Sentence) => React.ReactNode; live?: boolean }) {
  const prev = useRef(s.text)
  const restarts = useRef(0)
  if (!s.text.startsWith(prev.current)) restarts.current++
  prev.current = s.text
  return <PacedSentence key={restarts.current} s={s} done={done} start={restarts.current > 0 && live ? 0 : undefined} />
}

function PacedSentence({ s, done, start }: { s: Sentence; done: (s: Sentence) => React.ReactNode; start?: number }) {
  const { shown, settled, animating } = useSmoothText(s.text, !!s.partial, start)
  if (s.partial || animating) return <span className="ov-streaming"><Words text={shown} settled={settled} /></span>
  return <>{done(s)}</>
}

function toneOf(kind?: LineKind): Tone {
  if (kind === 'needs' || kind === 'stuck' || kind === 'stalled') return 'you'
  if (kind === 'working') return 'agent'
  if (kind === 'waiting') return 'waiting'
  return 'quiet'
}

export function Overview({ ws, row, track, onTrack }: {
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
    let timer: ReturnType<typeof setTimeout> | undefined
    // Each pass opens the recap stream: a result per change while it is being
    // written, then the stream ends. A wait or a failure is asked again soon.
    const pass = async (): Promise<number> => {
      controller = new AbortController()
      const abort = setTimeout(() => controller?.abort(), 150_000)
      let last: RecapResult | null = null
      try {
        const r = await fetch(`${cfg.apiUrl}/api/overview`, {
          method: 'POST', headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
          body: JSON.stringify({ project: ws.project, bead_id: ws.bead_id, track_id: track ?? '', stream: true }), signal: controller.signal,
        })
        if (!r.ok) throw new Error('not ready')
        const take = (line: string) => {
          if (!line.trim()) return
          const body = JSON.parse(line) as RecapResult
          if (!['waiting', 'updating', 'retrying', 'ready', 'unavailable'].includes(body.state) || !Array.isArray(body.evidence)) throw new Error('Invalid recap')
          last = body
          if (!cancelled) { setResult(body); setError('') }
        }
        const reader = r.body?.getReader()
        if (!reader) take(await r.text())
        else {
          const dec = new TextDecoder()
          let buf = ''
          for (;;) {
            const { done, value } = await reader.read()
            buf += dec.decode(value, { stream: !done })
            const lines = buf.split('\n')
            buf = lines.pop() ?? ''
            lines.forEach(take)
            if (done) break
          }
          take(buf)
        }
      } catch {
        if (!cancelled) setError('reconnecting')
        return 3_000
      } finally { clearTimeout(abort) }
      const state = (last as RecapResult | null)?.state
      return state === 'waiting' || state === 'retrying' || state === 'updating' ? 3_000 : 15_000
    }
    const loop = async () => {
      const wait = await pass()
      if (!cancelled) timer = setTimeout(() => void loop(), wait)
    }
    void loop()
    return () => { cancelled = true; clearTimeout(timer); controller?.abort() }
  }, [cfg, ws.project, ws.bead_id, track, degraded])

  // While the reader is writing, its draft stands in for the earlier recap.
  const draftOn = result?.state === 'updating' && !!result.draft?.summary && sentencesOf(result.draft.summary).length > 0
  const shown = draftOn ? { summary: result!.draft!.summary, changes: result!.draft!.changes ?? [], next: result!.draft!.next ?? [], generated_at: result?.recap?.generated_at ?? '' } : result?.recap

  // One number per source across the whole recap, so a source cited twice reads
  // as the same footnote.
  const sourceNumbers = useMemo(() => {
    const numbers = new Map<string, number>()
    const recap = shown
    if (recap) for (const c of [recap.summary, ...recap.changes, ...recap.next]) for (const s of sentencesOf(c)) for (const id of s.sources ?? []) if (!numbers.has(id)) numbers.set(id, numbers.size + 1)
    return numbers
  }, [result, shown])
  // The marks close their own sentence, held to its last word so they never sit alone on a line.
  const cited = (s: Sentence) => {
    let lastWord = s.text.lastIndexOf(' ') + 1
    // A last word inside a code span stays with it.
    if ((s.text.slice(0, lastWord).split('`').length - 1) % 2 === 1) lastWord = 0
    return (
      <>
        {withCode(s.text.slice(0, lastWord))}
        <span className="ov-cited">
          {withCode(s.text.slice(lastWord))}
          <span className="ov-cites">{(s.sources ?? []).map((id) => {
            const e = result?.evidence.find((x) => x.id === id)
            return e ? <button key={id} type="button" className={source?.evidence.id === id ? 'is-open' : ''} aria-label={`Evidence: ${e.label}`}
              onMouseEnter={(ev) => hoverOpen(e, ev.currentTarget)} onMouseLeave={hoverClose}
              onClick={(ev) => { cancelHover(); const anchor = ev.currentTarget; setSource((s) => (s?.pinned && s.evidence.id === id ? null : { evidence: e, anchor, pinned: true })) }}>{sourceNumbers.get(id)}</button> : null
          })}</span>
        </span>
      </>
    )
  }
  const cite = (claim: Claim) => sentencesOf(claim).map((s, n) => (
    <Fragment key={n}>
      {n > 0 && ' '}
      <StreamSentence s={s} done={cited} live={draftOn} />
    </Fragment>
  ))
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
          {shown ? <div>
            {(result?.stale || result?.state === 'updating') && !draftOn && <p className="ov-muted ov-refreshing" role="status"><Spinner size={10} /> Refreshing summary…</p>}
            <p className="ov-summary">{cite(shown.summary)}</p>
            {shown.changes.length > 0 && <><h2>What changed</h2><ul>{shown.changes.map((c, i) => <li key={i}>{cite(c)}</li>)}</ul></>}
            {shown.next.length > 0 && <><h2>What happens next</h2><ul>{shown.next.map((c, i) => <li key={i}>{cite(c)}</li>)}</ul></>}
          </div> : <p className="ov-muted" role="status">{waitText(result, degraded, !!error)}</p>}
          {shown && result?.state === 'retrying' && <p className="ov-warning">{waitText(result, false, false)}</p>}
        </section>
        {below.length > 0 && <section aria-label="Workstreams under this one"><h2>Across this workstream</h2><div className="ov-tracks">{below.map((t) => <button type="button" key={t.bead_id} onClick={() => onTrack(t.bead_id)}><strong>{plainTitle(t.title)}</strong><span data-tone={toneOf(t.kind)}>{t.text}</span></button>)}</div></section>}
        <footer className="ov-footer">
          {ws.repo && /^[\w.-]+\/[\w.-]+$/.test(ws.repo) && artifacts.map((p) => <a key={p} href={`https://github.com/${ws.repo}/pull/${p}`} target="_blank" rel="noreferrer">PR #{p} ↗</a>)}
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

// A recap that cannot be drawn must never take the app down with it.
export class OverviewBoundary extends Component<{ children: React.ReactNode }, { failed: boolean }> {
  state = { failed: false }
  static getDerivedStateFromError() { return { failed: true } }
  render() {
    return this.state.failed ? <div className="ov-pane" role="alert">The summary could not be shown.</div> : this.props.children
  }
}
