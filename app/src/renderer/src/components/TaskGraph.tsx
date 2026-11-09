// The right panel's Graph tab: the beads for the selection (every workstream
// for Commander, one workstream otherwise), raw. A box is a bead with
// children, an arrow is a real "blocks" dependency, a tag names the agent. Drag to pan, pinch or Ctrl+scroll to zoom, Fit to see it all.

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { GateChecklist } from './GateChecklist'
import { buildCanvas, headH, type Box, type Canvas, type Card, type Marker } from '../graph'
import type { GraphLevel } from '../../../shared/snapshot'
import { useRepo, useStore } from '../store'
import { approverNames, askKey, clock, selectionKey, wsKey } from '../viewmodel'
import { PrPill } from '../../../features/github/PrPill'
import { splitTickets, TicketPills } from '../../../features/linear/Ticket'

const LEVELS: [GraphLevel, string, string][] = [
  [1, 'Gates', 'Only the gates, in order, with who they wait on'],
  [2, 'Stages', 'Gates, the stages they guard, and the work under way'],
  [3, 'All', 'Every bead'],
]

const MIN_ZOOM = 0.2
const MAX_ZOOM = 2
const FIT_MIN_ZOOM = 0.8

interface View {
  x: number
  y: number
  k: number
}

function useFocus(): string | undefined {
  const snapshot = useStore((s) => s.snapshot)
  const selected = useStore((s) => s.selected)
  const linked = useStore((s) => s.graphFocus)
  // A link clicked in a chat moves the graph until the selection changes.
  if (linked && linked.sel === selectionKey(selected)) return linked.ws
  if (selected.kind === 'ws') return selected.key
  if (selected.kind === 'agent') return selected.ws
  if (selected.kind === 'ask' && snapshot) {
    const a = snapshot.needs_you.find((n) => askKey(n) === selected.key)
    const w = a && snapshot.workstreams.find((x) => x.project === a.project && x.title === a.workstream)
    return w ? wsKey(w) : undefined
  }
  return undefined
}

export function TaskGraph() {
  const snapshot = useStore((s) => s.snapshot)
  const focus = useFocus()
  const host = useRef<HTMLDivElement>(null)
  // The fleet view wraps workstreams to the pane's shape: more columns when it is wide.
  const [cols, setCols] = useState(3)
  useLayoutEffect(() => {
    const el = host.current
    if (!el) return
    const measure = () => setCols(Math.max(1, Math.floor(el.clientWidth / 330)))
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // Gates (the five-second view), Stages, or All; one bead's row opens to the next level on its own.
  const [level, setLevel] = useState<GraphLevel>(1)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const toggleStage = useCallback((id: string) => setExpanded((e) => (e.has(id) ? new Set([...e].filter((x) => x !== id)) : new Set([...e, id]))), [])
  const canvas = useMemo(() => (snapshot ? buildCanvas(snapshot, focus, cols, { level, expanded }) : null), [snapshot, focus, cols, level, expanded])
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 })

  const fit = useCallback(() => {
    const el = host.current
    if (!el || !canvas) return
    const pad = 32
    // Never shrink below readable: a tree too big for the pane starts at its
    // top left at a legible size, and you pan for the rest.
    const k = Math.max(FIT_MIN_ZOOM, Math.min((el.clientWidth - pad * 2) / canvas.width, (el.clientHeight - pad * 2) / canvas.height, 1))
    setView({ k, x: Math.max(pad, (el.clientWidth - canvas.width * k) / 2), y: Math.max(pad, (el.clientHeight - canvas.height * k) / 2) })
  }, [canvas])

  // A new focus (another workstream, or Commander) frames the whole tree, as
  // does the first time there is a tree to frame.
  const ready = !!canvas
  useLayoutEffect(fit, [focus, ready, cols, level]) // eslint-disable-line react-hooks/exhaustive-deps

  // A link to a bead centres it, at a readable zoom, and marks it for a moment.
  const linked = useStore((s) => s.graphFocus)
  const [highlight, setHighlight] = useState<string>()
  const centred = useRef(0)
  useLayoutEffect(() => {
    const el = host.current
    if (!linked?.bead || !canvas || !el || focus !== linked.ws || centred.current === linked.at) return
    centred.current = linked.at
    const id = `${linked.ws}:${linked.bead}`
    const r = canvas.cards.find((c) => c.id === id) ?? canvas.boxes.find((b) => b.id === id)
    if (!r) return
    const k = Math.max(view.k, FIT_MIN_ZOOM)
    setView({ k, x: el.clientWidth / 2 - (r.x + r.w / 2) * k, y: el.clientHeight / 3 - (r.y + Math.min(r.h, 120) / 2) * k })
    setHighlight(id)
  }, [linked, canvas, focus]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (!highlight) return
    const t = setTimeout(() => setHighlight(undefined), 2400)
    return () => clearTimeout(t)
  }, [highlight])

  // Never strand the view on empty space: if the graph changed shape (a
  // live reload, a new step) and nothing of it is on screen any more, frame it.
  useLayoutEffect(() => {
    const el = host.current
    if (!el || !canvas || !el.clientWidth || !el.clientHeight) return // not laid out (hidden, or a test)
    const left = view.x
    const top = view.y
    const right = view.x + canvas.width * view.k
    const bottom = view.y + canvas.height * view.k
    if (right < 40 || bottom < 40 || left > el.clientWidth - 40 || top > el.clientHeight - 40) fit()
  }, [canvas, view, fit])

  // Scroll pans, pinch (or Ctrl+scroll) zooms around the pointer, like Obsidian's canvas.
  useEffect(() => {
    const el = host.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      e.preventDefault()
      const r = el.getBoundingClientRect()
      setView((v) => {
        if (!e.ctrlKey && !e.metaKey) return { ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }
        const k = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, v.k * Math.exp(-e.deltaY * 0.01)))
        const px = e.clientX - r.left
        const py = e.clientY - r.top
        return { k, x: px - ((px - v.x) * k) / v.k, y: py - ((py - v.y) * k) / v.k }
      })
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  const drag = useRef<{ x: number; y: number } | null>(null)
  const onPointerDown = (e: React.PointerEvent) => {
    if ((e.target as HTMLElement).closest('button, a, [role=button]')) return
    drag.current = { x: e.clientX, y: e.clientY }
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = drag.current
    if (!d) return
    drag.current = { x: e.clientX, y: e.clientY }
    setView((v) => ({ ...v, x: v.x + e.clientX - d.x, y: v.y + e.clientY - d.y }))
  }
  const zoomBy = (f: number) => {
    const el = host.current
    if (!el) return
    setView((v) => {
      const k = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, v.k * f))
      const cx = el.clientWidth / 2
      const cy = el.clientHeight / 2
      return { k, x: cx - ((cx - v.x) * k) / v.k, y: cy - ((cy - v.y) * k) / v.k }
    })
  }

  if (!snapshot || !canvas) return null
  const dot = 22 * view.k

  return (
    <div
      ref={host}
      className={`canvas${drag.current ? ' panning' : ''}`}
      style={{ backgroundSize: `${dot}px ${dot}px`, backgroundPosition: `${view.x}px ${view.y}px` }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={() => (drag.current = null)}
      onPointerCancel={() => (drag.current = null)}
      onDoubleClick={(e) => !(e.target as HTMLElement).closest('button, a, [role=button]') && fit()}
    >
      <div className="canvas-world" style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})`, width: canvas.width, height: canvas.height }}>
        <CanvasWorld canvas={canvas} highlight={highlight} toggleStage={toggleStage} />
      </div>
      <div className="canvas-legend" aria-label="Legend">
        <span>
          <svg width="18" height="10" aria-hidden>
            <path d="M1 5 H13" />
            <path d="M12 1.5 L17 5 L12 8.5 z" className="fill" />
          </svg>
          unblocks
        </span>
        <span>
          <i className="legend-box" aria-hidden /> its child beads
        </span>
        <span>
          <i className="legend-diamond" aria-hidden /> approval gate
        </span>
        <GateLegend />
      </div>
      <div className="canvas-levels" role="group" aria-label="Graph level">
        {LEVELS.map(([n, word, hint]) => (
          <button key={n} type="button" aria-pressed={level === n} title={hint} onClick={() => setLevel(n)}>
            {word}
          </button>
        ))}
      </div>
      <div className="canvas-controls">
        <button type="button" aria-label="Zoom out" onClick={() => zoomBy(1 / 1.25)}>
          −
        </button>
        <button type="button" aria-label="Fit to view" onClick={fit}>
          Fit
        </button>
        <button type="button" aria-label="Zoom in" onClick={() => zoomBy(1.25)}>
          +
        </button>
      </div>
    </div>
  )
}

// Every state says what it means in words; the color only repeats it.
const STATE_WORD: Record<string, string> = {
  done: 'Done',
  'in-progress': 'In progress',
  review: 'In review',
  'not-started': 'Not started',
  blocked: 'Blocked',
  'needs-user': 'Needs you',
}

/**
 * BeadLine is the same second line on every bead: its beads status, its id,
 * and the Linear ticket and PR it is tied to. Whether an agent is working
 * this minute is the agent's tag, not the bead's.
 */
// A gate's state, in words: whether the person has approved it yet.
function gateWord(bead: Card | Box): string {
  if (bead.status === 'deferred') return bead.parkedUntil ? `Parked until ${clock(bead.parkedUntil)}` : 'Parked'
  if (bead.gateNode) return bead.gateNode === 'passed' ? 'Passed' : bead.gateNode === 'failed' ? 'Failed' : bead.state === 'in-progress' ? 'Waiting' : 'Not reached'
  if (bead.state === 'done') return 'Approved'
  if (bead.state === 'needs-user') return 'Needs you'
  if (bead.state === 'in-progress' || bead.state === 'review' || bead.state === 'blocked') return `Waiting on ${approverNames(bead.approvers)}`
  return 'Not reached'
}

function BeadLine({ bead, tickets, prs = [] }: { bead: Card | Box; tickets: string[]; prs?: string[] }) {
  const repo = useRepo(bead.project)
  return (
    <span className="bead-line">
      {bead.change && <span className={`bead-change change-${bead.change}`}>{CHANGE_WORD[bead.change]}</span>}
      <span className={`bead-state state-${bead.state}`} title={bead.waiting ? `${STATE_WORD[bead.state] ?? bead.state} · waiting on ${bead.waiting}` : undefined}>
        <i aria-hidden />
        {bead.gate || bead.status === 'deferred' ? gateWord(bead) : (STATE_WORD[bead.state] ?? bead.state)}
        {bead.waiting && bead.state !== 'done' && <span className="bead-wait"> · waiting on {bead.waiting}</span>}
      </span>
      {bead.beadId && <span className="bead-id">{bead.beadId}</span>}
      <TicketPills tickets={tickets} prs={prs.filter((n) => n !== bead.review?.replace(/^#/, ''))} repo={repo} />
      {bead.review && <PrPill repo={repo} n={bead.review} />}
    </span>
  )
}

/** CanvasWorld draws a laid-out canvas: boxes, dependency arrows, cards. */
export function CanvasWorld({ canvas, highlight, toggleStage }: { canvas: Canvas; highlight?: string; toggleStage?: (id: string) => void }) {
  const rects = new Map<string, { x: number; y: number; w: number; h: number }>()
  for (const c of canvas.cards) rects.set(c.id, { x: c.x, y: c.y, w: c.w, h: c.h })
  for (const b of canvas.boxes) rects.set(b.id, b)
  return (
    <>
      {canvas.boxes.map((b) => (
        <BoxView key={b.id} box={b} highlight={b.id === highlight} />
      ))}
      <svg className="canvas-edges" width={canvas.width} height={canvas.height} aria-hidden>
        <defs>
          <marker id="canvas-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" markerHeight="7" orient="auto">
            <path d="M0 0 L8 4 L0 8 z" />
          </marker>
        </defs>
        {canvas.edges.map((e) => {
          const a = rects.get(e.from)
          const b = rects.get(e.to)
          if (!a || !b) return null
          // Only an arrow to the bead right above exists (graph.ts arrange): straight down.
          const x = a.x + a.w / 2
          return <path key={`${e.from}>${e.to}`} d={`M${x} ${a.y + a.h} V${b.y - 3}`} markerEnd="url(#canvas-arrow)" />
        })}
      </svg>
      {canvas.cards.map((c) => (
        <CardView key={c.id} card={c} highlight={c.id === highlight} toggleStage={toggleStage} />
      ))}
    </>
  )
}

const CHANGE_WORD = { add: 'Proposed: new', remove: 'Proposed: remove', edit: 'Proposed: change' } as const

const GATE_LABEL = { yours: 'Your greenlight', reviewer: 'Reviewer', operator: 'Operator', auto: 'Auto check' } as const

/** GateBand is a gate bead as a labelled strip: who it is for, where it stands, its whole title. */
function GateBand({ card, open, highlight }: { card: Card; open: () => void; highlight?: boolean }) {
  const repo = useRepo(card.project)
  const t = splitTickets(card.title)
  const kind = card.gateKind ?? 'operator'
  return (
    <div
      role="button"
      tabIndex={0}
      className={`canvas-card gate gate-band state-${card.state}${card.status === 'deferred' ? ' parked' : ''} gate-${kind}${card.tone ? ` tone-${card.tone}` : ''}${card.now ? ' now' : ''}${highlight ? ' highlight' : ''}`}
      style={{ left: card.x, top: card.y, width: card.w, height: card.h }}
      onClick={open}
      onKeyDown={(e) => e.key === 'Enter' && open()}
      title={card.detail ? `${card.title}\n\n${card.detail}` : card.title}
    >
      <span className="gate-band-head">
        <span className="gate-mark">{GATE_LABEL[kind]}</span>
        {card.approvers?.length && kind !== 'yours' ? <span className="gate-who">{approverNames(card.approvers)}</span> : null}
        <span className="gate-band-state">{card.state === 'done' ? '✓ ' : ''}{gateWord(card)}</span>
      </span>
      <span className="gate-band-body">
        {t.text}
        <TicketPills tickets={t.tickets} prs={t.prs} repo={repo} />
      </span>
      {card.inputs?.length ? (
        <span className="gate-inputs" aria-label="Inputs">
          {card.inputs.map((i) => (
            <span key={i.id} className={`gate-input state-${i.state}${i.muted ? ' muted' : ''}`} title={i.id}>
              <i aria-hidden />
              {splitTickets(i.title).text}
            </span>
          ))}
        </span>
      ) : null}
    </div>
  )
}

/** Markers lists a bead's blockers that are not drawn as arrows, one line each. */
function Markers({ markers }: { markers?: Marker[] }) {
  if (!markers?.length) return null
  return (
    <span className="spine-markers">
      {markers.map((m) => (
        <span key={m.id} className={`spine-marker${m.closed ? ' muted' : ''}`} title={`${m.title} (${m.id})`}>
          {m.closed ? '✓ ' : 'blocked by '}
          {splitTickets(m.title).text}
        </span>
      ))}
    </span>
  )
}

/** SpineRow is one lifecycle stage on one line: its state and gate, its open tasks, the done ones as a count, and which stages block it. */
function SpineRow({ card, open, toggle, highlight }: { card: Card; open: () => void; toggle?: (id: string) => void; highlight?: boolean }) {
  const sp = card.spine!
  const t = splitTickets(card.title)
  const stage = card.beadId ?? ''
  return (
    <div
      role="button"
      tabIndex={0}
      className={`canvas-card spine-row state-${card.state}${card.status === 'deferred' ? ' parked' : ''}${card.gate ? ' gate' : ''}${card.tone ? ` tone-${card.tone}` : ''}${card.now ? ' now' : ''}${highlight ? ' highlight' : ''}`}
      style={{ left: card.x, top: card.y, width: card.w, height: card.h }}
      onClick={open}
      onKeyDown={(e) => e.key === 'Enter' && open()}
      title={card.title}
    >
      <span className="spine-head">
        <i className="spine-dot" aria-hidden />
        <span className="spine-title">{t.text}</span>
        {card.gate && <span className="spine-gate">{GATE_LABEL[card.gateKind ?? 'operator']}</span>}
        <span className="spine-state">{card.gate || card.status === 'deferred' ? gateWord(card) : (STATE_WORD[card.state] ?? card.state)}</span>
      </span>
      <Markers markers={card.markers} />
      {(sp.tasks.length > 0 || sp.done > 0 || sp.more > 0 || sp.guards) && (
        <span className="spine-tasks">
          {sp.guards && <span className="spine-guards">guards {splitTickets(sp.guards).text}</span>}
          {sp.tasks.map((x) => (
            <span key={x.id} className={`spine-task state-${x.state}${x.status === 'deferred' ? ' parked' : ''}`} title={x.title}>
              <i aria-hidden />
              {splitTickets(x.title).text}
            </span>
          ))}
          {(sp.done > 0 || sp.more > 0) && (
            <span className="spine-sum">
              <button type="button" aria-expanded={sp.expanded} onClick={(e) => (e.stopPropagation(), toggle?.(stage))}>
                {sp.expanded ? 'Show less' : [sp.more ? `+${sp.more} more` : '', sp.done ? `${sp.done} done` : ''].filter(Boolean).join(' · ')}
              </button>
            </span>
          )}
        </span>
      )}
    </div>
  )
}

function CardView({ card, highlight, toggleStage }: { card: Card; highlight?: boolean; toggleStage?: (id: string) => void }) {
  const working = useAgent(card.agent ?? '', card.project, card.state === 'in-progress' && !card.gone).state === 'working' && !!card.agent
  const snapshot = useStore((s) => s.snapshot)
  const select = useStore((s) => s.select)
  const t = splitTickets(card.title)
  const open = () => {
    if (card.ask && card.project && card.beadId) {
      const key = askKey({ project: card.project, bead_id: card.beadId })
      if (snapshot?.needs_you.some((a) => askKey(a) === key)) return select({ kind: 'ask', key })
    }
    if (card.wsKey) select({ kind: 'ws', key: card.wsKey })
  }
  if (card.spine) return <SpineRow card={card} open={open} toggle={toggleStage} highlight={highlight} />
  if (card.gate) return <GateBand card={card} open={open} highlight={highlight} />
  return (
    <div
      role="button"
      tabIndex={0}
      className={`canvas-card state-${card.state}${card.status === 'deferred' ? ' parked' : ''}${card.tone ? ` tone-${card.tone}` : ''}${working ? ' working' : ''}${card.now ? ' now' : ''}${card.change ? ` change-${card.change}` : ''}${highlight ? ' highlight' : ''}`}
      style={{ left: card.x, top: card.y, width: card.w, height: card.h }}
      onClick={open}
      onKeyDown={(e) => e.key === 'Enter' && open()}
      title={card.title}
    >
      <span className="canvas-card-head">
        <span className="canvas-card-title">
          {t.text}
        </span>
        {card.agent && card.state !== 'done' && <AgentAvatar role={card.agent} wsKey={card.wsKey} project={card.project} active={card.state === 'in-progress' && !card.gone} />}
      </span>
      <BeadLine bead={card} tickets={t.tickets} prs={t.prs} />
      {card.gates && <GateChecklist gates={card.gates} tones={card.gateTones} />}
      <Markers markers={card.markers} />
    </div>
  )
}

/** BoxView is a bead with children (a workstream is one), holding them. */
function BoxView({ box, highlight }: { box: Box; highlight?: boolean }) {
  const select = useStore((s) => s.select)
  const selected = useStore((s) => s.selected)
  const current = box.kind === 'workstream' && (selected.kind === 'ws' || selected.kind === 'agent') && box.wsKey === (selected.kind === 'ws' ? selected.key : selected.ws)
  const t = splitTickets(box.title)
  return (
    <div className={`canvas-box box-${box.kind} state-${box.state}${box.status === 'deferred' ? ' parked' : ''}${box.track ? ' is-track' : ''}${current ? ' current' : ''}${box.change ? ` change-${box.change}` : ''}${highlight ? ' highlight' : ''}`} style={{ left: box.x, top: box.y, width: box.w, height: box.h }}>
      <div className="canvas-box-head" style={{ height: headH(box) }}>
        <span className="canvas-card-head">
          <button type="button" className="canvas-box-title" onClick={() => box.wsKey && select({ kind: 'ws', key: box.wsKey })} title={box.title}>
            {box.track && <span className="track-tag">Workstream</span>}
            {t.text}
          </button>
          <AvatarStack roles={box.crew} wsKey={box.wsKey} project={box.project} />
        </span>
        <BeadLine bead={box} tickets={t.tickets} prs={t.prs} />
        {box.gates && <GateChecklist gates={box.gates} tones={box.gateTones} />}
        <Markers markers={box.markers} />
        {box.hidden ? <span className="spine-markers hidden-note">{box.hidden} other bead{box.hidden === 1 ? '' : 's'} hidden at this level</span> : null}
      </div>
    </div>
  )
}

// who is an agent role as words: "Background jobs Manager", "Operator · get ci green",
// "You". An Operator's role is op-<task words>-<its bead id>; the bead id
// starts with the project name. A Manager is named for its workstream.
function who(role: string, project = '', workstream = ''): string {
  if (role === 'user') return 'You'
  if (role === 'commander') return 'Commander'
  if (role.startsWith('manager-')) return workstream ? `${workstream} Manager` : 'Manager'
  const rest = role.replace(/^operator-/, '').replace(/^op-/, '')
  const slug = project && rest.includes(`-${project}-`) ? rest.slice(0, rest.indexOf(`-${project}-`)) : rest
  return `Operator · ${slug.split('-').filter(Boolean).join(' ')}`
}

// A role's colour: the same agent is the same colour everywhere.
function hue(role: string): number {
  let h = 0
  for (const c of role) h = (h * 31 + c.charCodeAt(0)) % 360
  return h
}

/**
 * AgentAvatar is who is on a bead: a circle in the agent's colour, the same
 * everywhere. While the agent works, a soft halo pulses out of it. Hover for
 * the name and state; an Operator's opens its chat.
 */
// liveRole finds the role as cmdrd runs it: a bead's owner label has the
// Operator's dots turned to dashes (labels allow no dots).
function liveRole(roles: string[], owner: string): string {
  return roles.includes(owner) ? owner : (roles.find((r) => r.replace(/\./g, '-') === owner) ?? owner)
}

// useAgent is an agent's live state as the graph shows it. Operators run
// outside the chat engine; an Operator owns a bead only while its process
// lives (cmdrd's reaper), so on unfinished work it is working.
function useAgent(owner: string, project?: string, active?: boolean) {
  const role = useStore((s) => liveRole([...Object.keys(s.snapshot?.agents ?? {}), ...Object.keys(s.chats)], owner))
  const agent = useStore((s) => s.snapshot?.agents[role])
  const chat = useStore((s) => s.chats[role])
  const title = useStore((s) => s.snapshot?.workstreams.find((w) => w.manager_role === role)?.title)
  const name = who(role, project, title ? splitTickets(title).text : '')
  const state = agent?.state ?? (chat?.running ? chat.state || 'idle' : role.startsWith('operator-') && active ? 'working' : undefined)
  return { role, name, state }
}

export function AgentAvatar({ role: owner, wsKey: ws, project, active }: { role: string; wsKey?: string; project?: string; active?: boolean }) {
  const { role, name, state } = useAgent(owner, project, active)
  const select = useStore((s) => s.select)
  const operator = role.startsWith('operator-')
  const h = role === 'user' ? 25 : role === 'commander' || role.startsWith('manager-') ? 235 : hue(role)
  return (
    <span
      className={`avatar agent-${state ?? 'unknown'}`}
      style={{ '--hue': h } as React.CSSProperties}
      title={`${name}${state ? ` · ${state}` : ''}`}
      role={operator ? 'button' : undefined}
      onClick={(e) => {
        if (!operator || !ws) return
        e.stopPropagation()
        select({ kind: 'agent', key: role, ws })
      }}
    >
    </span>
  )
}

/** AvatarStack is everyone on a box and inside it, overlapping; past four, a count. */
function AvatarStack({ roles, wsKey: ws, project }: { roles: string[]; wsKey?: string; project?: string }) {
  if (!roles.length) return null
  const shown = roles.slice(0, 4)
  return (
    <span className="avatars">
      {shown.map((r) => (
        <AgentAvatar key={r} role={r} wsKey={ws} project={project} active />
      ))}
      {roles.length > shown.length && <span className="avatar avatar-more">+{roles.length - shown.length}</span>}
    </span>
  )
}

/** GateLegend is the gate colours, tiny (dashed is not yet reached). */
export function GateLegend() {
  return (
    <span className="gate-legend" aria-label="Gate colours">
      <span className="tone-red"><i aria-hidden /> waiting on you</span>
      <span className="tone-yellow"><i aria-hidden /> waiting on others</span>
      <span className="tone-green"><i aria-hidden /> approved</span>
      <span className="tone-idle"><i aria-hidden /> not reached</span>
    </span>
  )
}
