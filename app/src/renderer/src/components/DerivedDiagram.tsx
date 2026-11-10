// DerivedDiagram draws a workstream's (or track's) diagram exactly as cmdrd
// derived it from beads (display row `diagram`) as the pitch's execution
// workflow is drawn: one line per actor, each step on its actor's line in
// work order, an arrow at each handoff, checkpoints with who approves and
// what counts as passed (SequenceDiagram). Every word, actor and colour is
// cmdrd's; nothing is derived here.

import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { BoardRow, Diagram, DiagramStep, Workflow } from '../../../shared/snapshot'
import { SequenceDiagram, type Actor, type FlowStep } from './ExecutionFlow'
import { subWorkflow } from './WorkflowView'

const ACTOR_IDS = new Set<Actor>(['you', 'manager', 'operators', 'reviewers', 'automatic'])

/** diagramFor is the diagram a header click or a card shows: a nested workstream's own,
 *  else the row's, which is the one standing in for it when there is one. */
export function diagramFor(row: BoardRow | undefined, track?: string): Diagram | null {
  const d = row?.diagram ?? (row?.workflow ? fromWorkflow(row.workflow, row.bead_id, row.title) : undefined)
  if (!d) return null
  if (track) return d.tracks?.find((t) => t.id === track) ?? null
  if (row?.scope) return d.tracks?.find((t) => t.id === row.scope) ?? d
  const own = d.steps.filter((s) => s.kind !== 'done' && !s.track)
  const open = d.steps.filter((s) => s.kind !== 'done' && s.track)
  if (own.length === 0 && open.length === 1) return d.tracks?.find((t) => t.id === open[0].id) ?? d
  return d
}

// How many of the latest done steps always show, so every diagram reads as a sequence.
const RECENT_DONE = 4

const LANE = { you: 'you', manager: 'agents', operators: 'agents', reviewers: 'elsewhere', automatic: 'elsewhere' } as const

/** fromWorkflow is a diagram from a cmdrd that sends only the workflow: each
 *  step's line read off its own words, so a header click still draws the sequence. */
export function fromWorkflow(w: Workflow, id: string, title: string): Diagram {
  const actorOf = (gate: boolean, who: string, kind: string, text: string): Actor => {
    if (gate) return /\byou\b/.test(who) ? 'you' : /manager/i.test(who) ? 'manager' : /auto|ci\b/i.test(who) ? 'automatic' : 'reviewers'
    if (kind === 'needs') return 'you'
    if (/^(Result ready for Manager review|Manager review overdue|Waiting for Manager to close)/.test(text)) return 'manager'
    if (/^Operator|Waiting for an Operator/.test(text)) return 'operators'
    if (/^Waiting for review/.test(text)) return 'reviewers'
    if (/^Waiting for (CI|main CI)|^Waiting for .* · /.test(text)) return 'automatic'
    return 'manager'
  }
  const step = (s: Workflow['steps'][number]): DiagramStep => {
    const actor = actorOf(!!s.gate, s.who ?? '', s.kind, s.text)
    return { id: s.id, title: s.title, kind: s.kind, text: s.text, why: s.why, lane: LANE[actor], actor, gate: s.gate, approvers: s.who, track: s.track, review: s.review }
  }
  const tracks = w.steps.filter((s) => s.track && s.children?.length).map((t) => {
    const sub = subWorkflow(w, t.id)!
    return { id: t.id, title: t.title, current: sub.current, steps: sub.steps.map(step) }
  })
  return { id, title, current: w.current, steps: w.steps.map(step), tracks }
}

/** shownSteps is every open step and the latest done ones; earlier done steps fold unless asked for. */
export function shownSteps(d: Diagram, showEarlier: boolean): { steps: DiagramStep[]; earlier: number; done: number } {
  const doneAt = d.steps.flatMap((s, i) => (s.kind === 'done' ? [i] : []))
  const hidden = new Set(showEarlier ? [] : doneAt.slice(0, Math.max(0, doneAt.length - RECENT_DONE)))
  return { steps: d.steps.filter((_, i) => !hidden.has(i)), earlier: hidden.size, done: doneAt.length }
}

/** nowLine is the one line above a diagram: the step the work is on now, in its own words. */
export function nowLine(d: Diagram): string {
  const s = d.steps.find((x) => x.id === d.current)
  return s ? `${s.title} · ${s.text}` : ''
}

// approves is a checkpoint's "<who> approve(s)".
function approves(s: DiagramStep): string {
  if (s.actor === 'automatic') return s.who ?? 'automatic'
  if (s.actor === 'manager') return 'Manager approves'
  const who = s.approvers || 'nobody'
  if (who === 'you') return 'you approve'
  return `${who} ${who.includes(',') ? 'approve' : 'approves'}`
}

/** flowOf is a derived diagram as the sequence diagram's steps, in time order:
 *  what is done, then Now, then the rest happening now, then what is ahead,
 *  parked steps last. Nothing above Now is ever open. */
export function flowOf(d: Diagram, steps: DiagramStep[] = d.steps): FlowStep[] {
  // Every step on its own line. An open stage is not a step of its own: its
  // steps are drawn, each marked with the stage it sits in ("Under …").
  const flat: { s: DiagramStep; inside?: string }[] = []
  const walk = (ss: DiagramStep[], inside?: string) => {
    for (const s of ss) {
      if (!(s.children?.length && s.kind !== 'done')) flat.push({ s, inside })
      walk(s.children ?? [], s.title)
    }
  }
  walk(steps)
  const parked = (s: DiagramStep) => s.kind === 'idle' && s.text.startsWith('Parked')
  const live = (s: DiagramStep) => s.kind !== 'done' && s.kind !== 'idle'
  // Now is every step that asks the user (each one theirs to act on now, so
  // each is red); with none, cmdrd's current step when it is live, then work
  // in hand, then a wait on someone else, a stalled step last; else the first
  // step ahead.
  const cur = flat.find((x) => x.s.id === d.current)?.s
  const first = (kind: string) => flat.find((x) => x.s.kind === kind)?.s
  const asks = flat.filter((x) => x.s.kind === 'needs').map((x) => x.s.id)
  const one = cur && live(cur) ? cur : (first('working') ?? first('waiting') ?? flat.find((x) => live(x.s))?.s ?? flat.find((x) => x.s.kind !== 'done' && !parked(x.s))?.s)
  const now = new Set(asks.length ? asks : one ? [one.id] : [])
  const rank = (s: DiagramStep) => (s.kind === 'done' ? 0 : now.has(s.id) ? 1 : live(s) ? 2 : parked(s) ? 4 : 3)
  const ordered = flat.map((x, i) => ({ ...x, i })).sort((a, b) => rank(a.s) - rank(b.s) || a.i - b.i)
  return ordered.map(({ s, inside }) => {
    const actor: Actor = ACTOR_IDS.has(s.actor as Actor) ? (s.actor as Actor) : 'manager'
    const detail = s.gate ? '' : (s.who ?? '')
    const { kind, text } = s
    return {
      id: s.id,
      title: s.title,
      actor,
      checkpoint: !!s.gate,
      done: s.kind === 'done',
      now: now.has(s.id),
      criteria: s.gate ? (s.criteria ? `Passes when: ${s.criteria}` : 'No criteria yet') : '',
      who: s.gate ? approves(s) : undefined,
      kind,
      text,
      meta: [s.why, detail, s.progress].filter(Boolean).join(' · ') || (s.actor ? undefined : 'Nothing in beads says whose move this is'),
      track: s.track,
      noActor: !s.actor,
      inside,
      review: s.review,
    }
  })
}

/** scrollParent is the nearest ancestor that scrolls (null = the page). */
function scrollParent(el: HTMLElement): HTMLElement | null {
  for (let p = el.parentElement; p; p = p.parentElement) {
    if (/auto|scroll/.test(getComputedStyle(p).overflowY)) return p
  }
  return null
}

const reducedMotion = () => typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

/** useOffscreen reports which edge the current step is scrolled past: 'above', 'below', or null while it shows.
 *  It also sets --dg-pin on the diagram: the height of the pinned lane headers, so the up arrow sits below them. */
// The step the arrow brings back: one waiting on the user first, else where the work is now.
const NEEDS_STEP = '.xf-step.kind-needs:not(.is-done)'
const NOW_STEP = '.xf-step.is-now, .xf-step.is-next'

function useOffscreen(ref: React.RefObject<HTMLDivElement | null>, watch: unknown): { edge: 'above' | 'below' | null; jump: () => void; needs: boolean } {
  const [edge, setEdge] = useState<'above' | 'below' | null>(null)
  const target = () => ref.current?.querySelector<HTMLElement>(NEEDS_STEP) ?? ref.current?.querySelector<HTMLElement>(NOW_STEP) ?? null
  // The scroll check: the Now step against the scroller's rect. It backs up IntersectionObserver, whose callbacks some environments never fire.
  const measurePin = () => {
    const host = ref.current
    const pin = host?.querySelector<HTMLElement>('.xf-actor')?.getBoundingClientRect().height ?? 0
    host?.style.setProperty('--dg-pin', `${Math.ceil(pin)}px`)
  }
  const check = () => {
    const el = target()
    const host = ref.current
    if (!el || !host) return setEdge(null)
    const root = scrollParent(host)
    const box = root ? root.getBoundingClientRect() : { top: 0, bottom: window.innerHeight }
    measurePin()
    const r = el.getBoundingClientRect()
    setEdge(r.bottom <= box.top ? 'above' : r.top >= box.bottom ? 'below' : null)
  }
  const observed = useRef<Element | null>(null)
  const io = useRef<IntersectionObserver | null>(null)
  // After every render: follow the DOM node of the Now step (a redraw can replace it under the same key).
  useEffect(() => {
    const el = target()
    if (el !== observed.current) {
      io.current?.disconnect()
      observed.current = el
      io.current = null
      if (el && typeof IntersectionObserver !== 'undefined') {
        const root = ref.current ? scrollParent(ref.current) : null
        io.current = new IntersectionObserver(
          (entries) => {
            const e = entries[entries.length - 1]
            if (!e) return
            if (e.isIntersecting) return setEdge(null)
            const top = e.rootBounds?.top ?? root?.getBoundingClientRect().top ?? 0
            setEdge(e.boundingClientRect.top < top ? 'above' : 'below')
          },
          { root },
        )
        io.current.observe(el)
      }
    }
    if (!io.current) check()
    else measurePin()
  }) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const host = ref.current
    const root = host ? scrollParent(host) : null
    const on = () => check()
    ;(root ?? window).addEventListener('scroll', on, { passive: true })
    window.addEventListener('resize', on)
    return () => {
      ;(root ?? window).removeEventListener('scroll', on)
      window.removeEventListener('resize', on)
      io.current?.disconnect()
      io.current = null
      observed.current = null
    }
  }, [watch]) // eslint-disable-line react-hooks/exhaustive-deps
  const jump = () => target()?.scrollIntoView?.({ block: 'center', inline: 'nearest', behavior: reducedMotion() ? 'auto' : 'smooth' })
  return { edge, jump, needs: !!ref.current?.querySelector(NEEDS_STEP) }
}

export function DerivedDiagram({ diagram, onOpenTrack }: { diagram: Diagram; onOpenTrack?: (id: string) => void }) {
  // It opens on where the work is now, centred; later snapshots leave the scroll alone.
  const ref = useRef<HTMLDivElement>(null)
  useLayoutEffect(() => {
    ref.current?.querySelector('.xf-step.is-now, .xf-step.is-next')?.scrollIntoView?.({ block: 'center', inline: 'nearest' })
  }, [])
  const flow = flowOf(diagram)
  // Re-observe when the step that is Now changes or the diagram redraws.
  const { edge, jump, needs } = useOffscreen(ref, flow.map((f) => `${f.id}${f.now ? '*' : ''}`).join('|'))
  if (diagram.steps.length === 0) return <p className="dg-empty">Nothing planned yet</p>
  // Always at least two lines, so it reads as a sequence: yours and the agents'.
  const used = new Set(flow.map((f) => f.actor))
  const ensure: Actor[] = used.size >= 2 ? [] : used.has('you') ? ['manager'] : ['you']
  const button = (at: 'above' | 'below') => (
    <div className={`dg-jump-rail ${at}`}>
      {edge === at && (
        <button
          type="button"
          className={`dg-jump${needs ? ' needs' : ''}`}
          aria-label={needs ? 'Jump to the step waiting on you' : 'Jump to the current step'}
          title={needs ? `Waiting on you, ${at}` : `Current step, ${at}`}
          onClick={jump}
        >
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
            <path d={at === 'below' ? 'M8 3v9.5M3.5 8.5 8 13l4.5-4.5' : 'M8 13V3.5M3.5 7.5 8 3l4.5 4.5'} fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </button>
      )}
    </div>
  )
  return (
    <div className="xf dg" ref={ref}>
      {button('above')}
      <SequenceDiagram steps={flow} ensure={ensure} onOpen={onOpenTrack} />
      {button('below')}
    </div>
  )
}
