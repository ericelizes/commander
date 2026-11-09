// The right column's Graph: each workstream's tracks as lanes read top to
// bottom, gate, the work between, gate, the work between, ... A gate says who
// approves it and where it stands (approved, your OK now, once the work above
// is done, later); the work between lists its open steps with who has them or
// what they wait on, done folded to a count. Click a step to open its own. Every workstream sits on one canvas; selecting one glides to it, and
// Commander zooms out to all of them.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { splitTickets } from '../../../features/linear/Ticket'
import { approverWords, flowVM, laneStages, openChildren, type BriefRow, type StageRow } from '../flow'
import type { BoardRow, SnapStep, SnapWorkstream } from '../../../shared/snapshot'
import { useStore } from '../store'
import { askKey, board, fmtAge, selectionKey, percent, wsKey } from '../viewmodel'

interface View {
  x: number
  y: number
  k: number
}

const MIN_ZOOM = 0.2
const MAX_ZOOM = 1.6
const PAD = 28

export function FlowGraph() {
  const snapshot = useStore((s) => s.snapshot)
  const nowSec = useStore((s) => s.nowSec)
  const selected = useStore((s) => s.selected)
  const linked = useStore((s) => s.graphFocus)
  const nav = snapshot ? board(snapshot) : null

  let key = ''
  if (linked && linked.sel === selectionKey(selected)) key = linked.ws
  else if (selected.kind === 'ws') key = selected.key
  else if (selected.kind === 'agent') key = selected.ws
  else if (selected.kind === 'ask' && snapshot) {
    const a = snapshot.needs_you.find((n) => askKey(n) === selected.key)
    const w = a && snapshot.workstreams.find((x) => x.project === a.project && x.title === a.workstream)
    if (w) key = wsKey(w)
  }

  const host = useRef<HTMLDivElement>(null)
  const world = useRef<HTMLDivElement>(null)
  const [view, setView] = useState<View>({ x: PAD, y: PAD, k: 0.6 })
  const [gliding, setGliding] = useState(false)
  const glide = (v: View) => {
    setGliding(true)
    setView(v)
    window.setTimeout(() => setGliding(false), 450)
  }

  // frame fits a world rectangle into the pane: one workstream, or everything.
  const frame = (r: { x: number; y: number; w: number; h: number }, maxK: number, minK = MIN_ZOOM) => {
    const el = host.current
    if (!el || !el.clientWidth) return null
    const k = Math.min(maxK, Math.max(minK, Math.min((el.clientWidth - PAD * 2) / r.w, (el.clientHeight - PAD * 2) / r.h)))
    const x = r.w * k < el.clientWidth - PAD * 2 ? (el.clientWidth - r.w * k) / 2 - r.x * k : PAD - r.x * k
    return { k, x, y: PAD - r.y * k }
  }
  const rectOf = (sel: string) => {
    const w = world.current
    const el = sel ? (w?.querySelector(sel) as HTMLElement | null) : w
    if (!w || !el) return null
    let x = 0
    let y = 0
    for (let n: HTMLElement | null = el; n && n !== w; n = n.offsetParent as HTMLElement | null) {
      x += n.offsetLeft
      y += n.offsetTop
    }
    return { x, y, w: el.offsetWidth, h: el.offsetHeight }
  }
  const fitAll = () => {
    const r = rectOf('')
    const v = r && frame(r, 1)
    if (v) glide(v)
  }
  const fitKey = () => {
    if (!key) return fitAll()
    const r = rectOf(`[data-ws="${CSS.escape(key)}"]`)
    // A workstream zooms in to a readable size: it fills the pane when it fits,
    // else it starts at its top left and you pan for the rest.
    const v = r && frame({ ...r, h: Math.min(r.h, (host.current?.clientHeight ?? r.h) / 0.9) }, 1, 0.85)
    if (v) glide(v)
  }

  // A new selection glides to its workstream (or out to everything), as does the first frame there is to fit.
  const ready = !!nav
  useLayoutEffect(fitKey, [key, ready]) // eslint-disable-line react-hooks/exhaustive-deps

  // A link to a bead in a chat glides to it and marks it for a moment.
  const [highlight, setHighlight] = useState('')
  useEffect(() => {
    if (!linked?.bead) return
    const r = rectOf(`[data-ws="${CSS.escape(linked.ws)}"] [data-bead="${CSS.escape(linked.bead)}"]`)
    const el = host.current
    if (!r || !el) return
    const k = Math.max(view.k, 0.8)
    glide({ k, x: el.clientWidth / 2 - (r.x + r.w / 2) * k, y: el.clientHeight / 3 - r.y * k })
    setHighlight(linked.bead)
    const t = setTimeout(() => setHighlight(''), 2400)
    return () => clearTimeout(t)
  }, [linked?.at]) // eslint-disable-line react-hooks/exhaustive-deps

  // Scroll pans, pinch (or Ctrl+scroll) zooms around the pointer.
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
  const [panning, setPanning] = useState(false)
  const onPointerDown = (e: React.PointerEvent) => {
    if ((e.target as HTMLElement).closest('button, a, [role=button]')) return
    drag.current = { x: e.clientX, y: e.clientY }
    setPanning(true)
    ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = drag.current
    if (!d) return
    drag.current = { x: e.clientX, y: e.clientY }
    setView((v) => ({ ...v, x: v.x + e.clientX - d.x, y: v.y + e.clientY - d.y }))
  }
  const endDrag = () => {
    drag.current = null
    setPanning(false)
  }
  const zoomBy = (f: number) => {
    const el = host.current
    if (!el) return
    const k = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, view.k * f))
    const cx = el.clientWidth / 2
    const cy = el.clientHeight / 2
    glide({ k, x: cx - ((cx - view.x) * k) / view.k, y: cy - ((cy - view.y) * k) / view.k })
  }

  if (!snapshot || !nav) return <div className="flow flow-empty">Loading…</div>
  const dot = 22 * view.k
  return (
    <div
      ref={host}
      className={`canvas flow-canvas${panning ? ' panning' : ''}`}
      style={{ backgroundSize: `${dot}px ${dot}px`, backgroundPosition: `${view.x}px ${view.y}px` }}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={(e) => !(e.target as HTMLElement).closest('button, a, [role=button]') && fitKey()}
    >
      <div ref={world} className={`canvas-world flow-world${gliding ? ' gliding' : ''}`} style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}>
        {nav.workstreams.map((w) => {
          const ws = snapshot.workstreams.find((x) => wsKey(x) === w.key)
          return ws ? <Block key={w.key} ws={ws} vm={w} selected={w.key === key} nowSec={nowSec} highlight={highlight} /> : null
        })}
      </div>
      <div className="canvas-controls">
        <button type="button" aria-label="Zoom out" onClick={() => zoomBy(1 / 1.25)}>
          −
        </button>
        <button type="button" aria-label="Fit to view" onClick={fitKey}>
          Fit
        </button>
        <button type="button" aria-label="Show everything" onClick={fitAll}>
          All
        </button>
        <button type="button" aria-label="Zoom in" onClick={() => zoomBy(1.25)}>
          +
        </button>
      </div>
    </div>
  )
}

// Block is one workstream on the canvas: its header, then a lane per track.
function Block({ ws, vm, selected, nowSec, highlight }: { ws: SnapWorkstream; vm: BoardRow; selected: boolean; nowSec: number; highlight: string }) {
  const snapshot = useStore((s) => s.snapshot)
  const lanes = useMemo(() => flowVM(ws), [ws])
  const stages = useMemo(() => laneStages(ws), [ws])
  const asks = (snapshot?.needs_you ?? []).filter((a) => a.project === ws.project && a.workstream === ws.title)
  const askFor = (bead: string) => asks.find((a) => a.bead_id === bead)
  return (
    <section className={`flow-block${selected ? ' selected' : ''}`} data-ws={vm.key} aria-label={vm.title}>
      <header className="flow-head">
        <div className="flow-title">
          <span className="rank">{vm.n}</span>
          <button type="button" className="flow-block-title" onClick={() => useStore.getState().clickWorkstream(vm.key)}>
            {splitTickets(vm.title).text}
          </button>
          <button type="button" className="flow-link-btn" onClick={() => useStore.getState().select({ kind: 'agent', key: ws.manager_role, ws: vm.key })}>
            Talk to its Manager
          </button>
        </div>
        <StateText line={vm} nowSec={nowSec} />
        <div className="flow-meta">
          <span>
            {vm.progress.done} of {vm.progress.total} done
          </span>
          <span className="flow-bar" aria-hidden>
            <span style={{ width: `${percent(vm.progress)}%` }} />
          </span>
          {asks.length > 0 && <span className="flow-asks">⚑ {asks.length} need you</span>}
        </div>
      </header>
      <div className="flow-lanes">
        {lanes.map((l) => (
          <Lane key={l.key} title={l.title} rows={stages[l.key] ?? []} highlight={highlight} askFor={askFor} />
        ))}
      </div>
    </section>
  )
}


// Lane is one track as a short list of stages, one row each: what is done
// folds to a count, the current stage leads with its open work shown, the
// rest follow one line each and open on click.
function Lane({ title, rows, highlight, askFor, explain = false }: { title: string; rows: StageRow[]; highlight: string; askFor: (bead: string) => { project: string; bead_id: string } | undefined; explain?: boolean }) {
  const [showDone, setShowDone] = useState(false)
  const done = rows.filter((r) => r.status === 'done')
  const open = rows.filter((r) => r.status !== 'done')
  return (
    <section className="flow-lane" aria-label={title || 'Stages'}>
      {title && <h3 className="lane-title">{splitTickets(title).text}</h3>}
      <ol className="stages">
        {done.length > 0 && (
          <li>
            <button type="button" className="stage-fold" aria-expanded={showDone} onClick={() => setShowDone(!showDone)}>
              <span className="stage-ico st-done">✓</span>
              {done.length} done
              <span className="lane-caret">{showDone ? '▾' : '▸'}</span>
            </button>
          </li>
        )}
        {showDone && done.map((r) => <Stage key={r.id} r={r} current={false} highlight={highlight === r.id} explain={explain} />)}
        {open.map((r, i) => (
          <Stage key={r.id} r={r} current={i === 0 || r.status === 'needs'} highlight={highlight === r.id} ask={askFor(r.id)} explain={explain} />
        ))}
        {rows.length === 0 && <li className="lane-empty">Nothing open</li>}
      </ol>
    </section>
  )
}

const STAGE_ICON: Record<StageRow['status'], string> = { done: '✓', needs: '⚑', working: '●', waiting: '●', missing: '!', next: '○', later: '○' }

// Stage is one row: icon, the gate's stage name, who has it or checks it; the
// condition under it; the current stage (and any that needs you) shows its
// open steps, the others open them on click.
function Stage({ r, current, highlight, ask, explain = false }: { r: StageRow; current: boolean; highlight: boolean; ask?: { project: string; bead_id: string }; explain?: boolean }) {
  const kids = openChildren(r.step)
  const [open, setOpen] = useState(false)
  const show = (current && r.status !== 'done') || open
  // A gate (a gate bead, or a stage named by its gate) reads as a gate: a
  // diamond, a GATE label, a band; the work steps between gates sit indented.
  const gate = !!r.step.gate || !!r.sub
  // In a pitch a gate says who approves it and what approving it means.
  const right = explain && r.step.gate ? whoApproves(r.step) : r.right
  return (
    <li className={`stage st-${r.status}${gate ? ' is-gate' : ' is-work'}${current ? ' current' : ''}${highlight ? ' flow-hl' : ''}`} data-bead={r.id}>
      <button type="button" className="stage-row" disabled={!kids.length || current} onClick={() => setOpen(!open)}>
        {gate ? <span className={`stage-diamond st-${r.status}`} aria-hidden /> : <span className={`stage-ico st-${r.status}`}>{STAGE_ICON[r.status]}</span>}
        <span className="stage-label">
          {gate && <span className="stage-kicker">CHECKPOINT</span>}
          {splitTickets(r.label).text}
        </span>
        {/* A short "who" sits at the right; a long one gets its own line, so it never squeezes the name. */}
        {right.length <= 22 ? <span className="stage-right">{right}</span> : <span />}
        {right.length > 22 && <span className="stage-right stage-right-long">{right}</span>}
        {r.sub && <span className="stage-sub">{splitTickets(r.sub).text}</span>}
        {explain && r.step.gate && r.step.note && <span className="stage-means">{r.step.note}</span>}
      </button>
      {ask && r.status === 'needs' && (
        <button type="button" className="flow-open flow-decide stage-decide" onClick={() => useStore.getState().select({ kind: 'ask', key: askKey(ask) })}>
          Decide →
        </button>
      )}
      {show && kids.length > 0 && <Steps steps={kids} />}
    </li>
  )
}

function whoApproves(s: SnapStep): string {
  const who = approverWords(s)
  if (who === 'you') return 'You approve'
  if (/^(automatic|auto|operator)$/i.test(who)) return 'Automatic'
  return `${who} approves`
}

// Workflow draws a workstream's lanes as stages, the same rows as the Graph
// tab: an agent presenting a plan in chat.
export function Workflow({ ws }: { ws: SnapWorkstream }) {
  const lanes = useMemo(() => flowVM(ws), [ws])
  const stages = useMemo(() => laneStages(ws), [ws])
  return (
    <div className="flow-lanes cc-workflow">
      {lanes.map((l) => (
        <Lane key={l.key} title={l.title} rows={stages[l.key] ?? []} highlight="" askFor={() => undefined} explain />
      ))}
    </div>
  )
}

// Steps is one level of a node's children; a step with its own opens the next level.
function Steps({ steps }: { steps: { row: BriefRow; step: SnapStep }[] }) {
  return (
    <ul className="flow-steps">
      {steps.map((c) => (
        <Step key={c.step.bead_id} c={c} />
      ))}
    </ul>
  )
}

function Step({ c, hl = false }: { c: { row: BriefRow; step: SnapStep }; hl?: boolean }) {
  const [open, setOpen] = useState(false)
  const kids = openChildren(c.step)
  return (
    <li className={`tone-${c.row.tone}${hl ? ' flow-hl' : ''}`} data-bead={c.step.bead_id}>
      <button type="button" className="flow-step" disabled={!kids.length} onClick={() => setOpen(!open)}>
        <span className="flow-step-dot" aria-hidden />
        <span className="flow-step-title">
          {kids.length > 0 && <span className="flow-caret">{open ? '▾' : '▸'}</span>}
          {splitTickets(c.row.title).text}
        </span>
        <span className="flow-step-state">{c.row.detail}</span>
      </button>
      {open && <Steps steps={kids} />}
    </li>
  )
}

function StateText({ line, nowSec }: { line: { kind: string; text: string; since: number }; nowSec: number }) {
  const text = line.text
  return (
    <div className={`ws-state flow-state agent-${line.kind}`}>
      <span className="ws-state-text">{text}</span>
      {line.since ? <span className="ws-state-age">{fmtAge(nowSec - line.since)}</span> : null}
    </div>
  )
}
