import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import { splitTickets } from '../../../features/linear/Ticket'
import { MyProgress, progressWorkstreams } from '../../../features/linear/MyProgress'
import { AllClear } from './AllClear'
import { useAgentRows } from './SettingsModal'
import { useStore } from '../store'
import { commanderName } from '../jurisdiction'
import { board, fmtAge, fmtUSD, finishedOperatorsVM, operatorsVM, percent, type FleetVM } from '../viewmodel'
import type { BoardRow, BoardTrack, Display, SnapStep, Snapshot } from '../../../shared/snapshot'
import { Spinner } from './Glyphs'
import { isMac } from '../host'
import { useApis } from '../api'
import { confetti } from '../confetti'
import { NeedsYouQueue, itemKey } from '../../../features/decisions'
import { askKey, wsKey, type Selection } from '../viewmodel'
import { applyMove, dropAt, flatRows, landed, placeArgv, without, withoutGone, type Drop, type NavNode } from './navDrag'

// Sidebar view choices survive a reload; storage can be unavailable, so it is best effort.
function usePref<T extends string>(key: string, initial: T): [T, (v: T) => void] {
  const [v, setV] = useState<T>(() => {
    try {
      return (localStorage.getItem(key) as T | null) ?? initial
    } catch {
      return initial
    }
  })
  const set = (next: T) => {
    setV(next)
    try {
      localStorage.setItem(key, next)
    } catch {
      // not persisted
    }
  }
  return [v, set]
}

// What the rows need from the sidebar's drag: which row is lifted, and which nested rows are open.
// showOwner: more than one Commander runs these rows, so each row names its own.
const NavContext = createContext<{ lifted: string; openNested: string; showOwner: boolean; isOpen(key: string): boolean; toggleOpen(key: string): void }>({
  lifted: '', openNested: '', showOwner: false, isOpen: () => false, toggleOpen: () => {},
})

// Where each drop line sits, in the row's own indent.
const lineLeft = (depth: number) => (depth === 0 ? 8 : 44 + (depth - 1) * 16)
// How far right a row must go before it nests under the one above: a drag rarely runs dead straight.
const INDENT = 40
const DRAG_START = 5
// How long a dropped row holds its new place while the board catches up.
const HOLD_MS = 60_000

interface Lift { node: NavNode; x: number; y: number; drop: Drop | null; lineY: number }

export function Navigator() {
  const conn = useStore((s) => s.conn)
  const completed = useStore((s) => s.completed)
  const snapshot = useStore((s) => s.snapshot)
  const nowSec = useStore((s) => s.nowSec)
  const selected = useStore((s) => s.selected)
  const cursor = useStore((s) => s.cursor)
  const workflowOpen = useStore((s) => s.workflowOpen)
  const fresh = useStore((s) => s.fresh)
  const real = snapshot?.display ? board(snapshot) : null
  // A dropped row is where it was let go, whatever the board still says.
  const [moved, setMoved] = useState<{ node: NavNode; drop: Drop; at: number } | null>(null)
  // A completed row leaves at once; if the board still lists it after this long its close did not hold, and it comes back.
  const goneFor = 120
  const vm = useMemo(() => {
    if (!real) return real
    const placed = moved ? applyMove(real.workstreams, moved.node, moved.drop) : real.workstreams
    return { ...real, workstreams: withoutGone(placed, (k) => !!completed[k] && nowSec - completed[k] / 1000 < goneFor) }
  }, [real, moved, completed, nowSec])
  const progress = useMemo(() => progressWorkstreams(snapshot?.workstreams ?? [], vm?.workstreams ?? []), [snapshot?.workstreams, vm])
  const listRef = useRef<HTMLDivElement>(null)
  const apis = useApis()
  const [tab, setTab] = usePref<'needs' | 'all'>('nav.tab', 'all')
  const [folded, setFolded] = usePref<string>('nav.tracksFolded', '')
  const foldedSet = useMemo(() => new Set(folded ? folded.split('\n') : []), [folded])
  const toggleTracks = (key: string) => {
    const next = new Set(foldedSet)
    if (!next.delete(key)) next.add(key)
    setFolded([...next].join('\n'))
  }

  const [nestedOpen, setNestedOpen] = usePref<string>('nav.nestedOpen', '')
  const openSet = useMemo(() => new Set(nestedOpen ? nestedOpen.split('\n') : []), [nestedOpen])
  const toggleOpen = (key: string) => {
    const next = new Set(openSet)
    if (!next.delete(key)) next.add(key)
    setNestedOpen([...next].join('\n'))
  }
  const [lift, setLift] = useState<Lift | null>(null)
  const [moveError, setMoveError] = useState('')
  const placing = useRef(false)

  // Hold a dropped row until the board shows it there; if it never does, say so.
  useEffect(() => {
    if (moved && real && landed(real.workstreams, moved.node, moved.drop)) setMoved(null)
  }, [real, moved])
  useEffect(() => {
    if (!moved) return
    const t = window.setTimeout(() => {
      setMoved(null)
      setMoveError(`"${moved.node.title}" was moved, but the board does not show it yet.`)
    }, HOLD_MS)
    return () => window.clearTimeout(t)
  }, [moved])

  const place = (dragged: NavNode, drop: Drop) => {
    if (!apis) return
    if (drop.parent?.depth === 0 && foldedSet.has(drop.parent.key)) toggleTracks(drop.parent.key)
    if (drop.parent && drop.parent.depth > 0 && !openSet.has(drop.parent.key)) toggleOpen(drop.parent.key)
    setMoved({ node: dragged, drop, at: Date.now() })
    setMoveError('')
    placing.current = true
    void (async () => {
      let failed = ''
      try {
        const r = await apis.cmdr.runCmd(placeArgv(dragged, drop))
        failed = cmdError(r.stdout) || cmdError(r.stderr) || (r.ok ? '' : r.error || `exit ${r.exit}`)
        const warnings = cmdWarnings(r.stdout)
        if (!failed && warnings.length) setMoveError(warnings.join(' '))
      } catch (e) {
        failed = `could not reach Commander: ${(e as Error).message ?? e}`
      }
      placing.current = false
      if (failed) {
        setMoved(null)
        setMoveError(/not allowed/i.test(failed) ? 'Moving rows needs the latest Commander install.' : `Could not move it: ${failed}`)
      }
    })()
  }

  // A press on a row becomes a drag once the pointer has moved; a plain click still opens the row.
  const startPress = (e: React.PointerEvent<HTMLDivElement>) => {
    const scroller = listRef.current
    const target = e.target as HTMLElement
    const rowEl = target.closest<HTMLElement>('[data-nav-key]')
    if (e.button !== 0 || !vm || !apis || !scroller || !rowEl || placing.current || target.closest('.ws-fold, .track-fold, .complete-btn')) return
    const flat = flatRows(vm.workstreams, (k) => foldedSet.has(k), (k) => openSet.has(k), (bead) => realParent(snapshot, bead))
    const node = flat.find((n) => n.key === rowEl.dataset.navKey)
    if (!node) return
    const startX = e.clientX
    const startY = e.clientY
    let lifted = false
    let last: Lift | null = null
    const onMove = (m: PointerEvent) => {
      if (!lifted && Math.hypot(m.clientX - startX, m.clientY - startY) < DRAG_START) return
      if (!lifted) {
        lifted = true
        document.body.classList.add('nav-dragging')
      }
      const box = scroller.getBoundingClientRect()
      if (m.clientY < box.top + 24) scroller.scrollTop -= 8
      else if (m.clientY > box.bottom - 24) scroller.scrollTop += 8
      const list = without(flat, node.key)
      const rects = list.map((n) => scroller.querySelector(`[data-nav-key="${CSS.escape(n.key)}"]`)?.getBoundingClientRect())
      let gap = rects.findIndex((r) => r && m.clientY < r.top + r.height / 2)
      if (gap < 0) gap = list.length
      const drop = dropAt(flat, node.key, gap, node.depth + Math.trunc((m.clientX - startX) / INDENT))
      const edge = gap < rects.length ? rects[gap]?.top ?? 0 : rects[rects.length - 1]?.bottom ?? 0
      last = { node, x: m.clientX, y: m.clientY, drop, lineY: edge - box.top + scroller.scrollTop }
      setLift(last)
    }
    const finish = (run: boolean) => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('keydown', onKey, true)
      document.body.classList.remove('nav-dragging')
      setLift(null)
      if (!lifted) return
      // The click this press ends in must not also open the row. A press let go
      // elsewhere ends in no click, and the next real one must still work.
      const swallow = (c: MouseEvent) => { c.stopPropagation(); c.preventDefault() }
      window.addEventListener('click', swallow, { capture: true, once: true })
      window.setTimeout(() => window.removeEventListener('click', swallow, { capture: true }), 50)
      if (run && last?.drop) place(node, last.drop)
    }
    const onUp = () => finish(true)
    const onKey = (k: KeyboardEvent) => {
      if (k.key === 'Escape') {
        k.stopPropagation()
        finish(false)
      }
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('keydown', onKey, true)
  }

  // Keep the keyboard cursor in view.
  useEffect(() => {
    listRef.current?.querySelector('[data-cursor="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [cursor])

  const working = useAgentRows().filter((r) => r.state === 'working').length
  if (snapshot?.storage === 'postgres' && conn !== 'live') return <nav className="nav" aria-label="Navigator"><p role="status">Live state unavailable. Reconnecting…</p></nav>
  if (snapshot?.degraded) return <nav className="nav" aria-label="Navigator"><p role="status">Live state unavailable. {snapshot.degraded.reason}</p></nav>
  if (!vm || !snapshot) return <NavSkeleton />
  const selKey = selected.kind === 'commander' || selected.kind === 'progress' ? 'commander' : selected.key
  // The row that is open is the one lit: a nested row when it can be seen, else the top-level row it sits in.
  // An open decision lights the row it sits in, the one its Overview is showing.
  const openAsk = selected.kind === 'ask' ? snapshot.needs_you.find((n) => askKey(n) === selected.key) : undefined
  const askWs = openAsk && snapshot.workstreams.find((w) => w.project === openAsk.project && w.title === openAsk.workstream)
  const litKey = askWs ? wsKey(askWs) : selKey
  const openTrack = openAsk ? openAsk.track_id || undefined : workflowOpen?.ws === selKey ? workflowOpen.track : undefined
  const openRow = vm.workstreams.find((w) => w.key === litKey)
  const openNested = openTrack && flatRows(openRow ? [openRow] : [], (k) => foldedSet.has(k), (k) => openSet.has(k)).some((n) => n.depth > 0 && n.bead === openTrack) ? openTrack : ''
  const mod = isMac() ? '⌘' : '⌃'

  // Both lists in priority order: asks by their workstream's rank (stable within one).
  const rank = new Map(vm.asks.map((a) => [a.key, a.rank]))
  const needs = [...snapshot.needs_you].sort((x, y) => (rank.get(askKey(x)) ?? 0) - (rank.get(askKey(y)) ?? 0))
  const freshAsk = vm.asks.some((a) => fresh[a.key])

  return (
    <nav className="nav" aria-label="Navigator">
      <MyProgress workstreams={progress} onOpen={() => useStore.getState().select({ kind: 'progress' })} />
      <div className={`nav-tabs${tab === 'all' ? ' on-right' : ''}`} role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'needs'}
          className={`nav-tab${tab === 'needs' ? ' active' : ''}${freshAsk && tab !== 'needs' ? ' pulse' : ''}`}
          onClick={() => setTab('needs')}
        >
          Needs you
          {vm.asks.length > 0 && <span className="nav-tab-count">{vm.asks.length}</span>}
        </button>
        <button type="button" role="tab" aria-selected={tab === 'all'} className={`nav-tab${tab === 'all' ? ' active' : ''}`} onClick={() => setTab('all')}>
          All
        </button>
      </div>
      {moveError && (
        <button type="button" className="nav-move-error" title="Dismiss" onClick={() => setMoveError('')}>{moveError}</button>
      )}
      <div className="nav-scroll" ref={listRef} onPointerDown={tab === 'all' ? startPress : undefined}>
        {tab === 'needs' && needs.length === 0 && <AllClear agents={working} />}
        {tab === 'needs' && needs.length > 0 &&
          (apis ? (
            <div className="nav-queue">
              <NeedsYouQueue
                items={needs}
                api={apis.cmdr}
                onOpen={(item) => useStore.getState().select({ kind: 'ask', key: askKey(item) })}
                selectedKey={selectedItemKey(snapshot.needs_you, selected)}
                now={nowSec * 1000}
              />
            </div>
          ) : (
            <AskRows vm={vm} selKey={selKey} cursor={cursor} fresh={fresh} />
          ))}
        {tab === 'all' && (
          <NavContext.Provider value={{ lifted: lift?.node.key ?? '', openNested, showOwner: new Set(vm.workstreams.map((w) => w.planning_commander).filter(Boolean)).size > 1, isOpen: (k) => openSet.has(k), toggleOpen }}>
            {vm.workstreams.map((w) => (
              <Workstream
                key={w.key}
                w={w}
                tracks={w.tracks}
                folded={foldedSet.has(w.key)}
                onToggle={() => toggleTracks(w.key)}
                selected={litKey === w.key && !openNested}
                cursor={cursor === w.key && !openNested}
                mod={mod}
              />
            ))}
          </NavContext.Provider>
        )}
        {lift?.drop && <div className="nav-drop-line" style={{ top: lift.lineY, left: lineLeft(lift.drop.depth) }} aria-hidden />}
      </div>
      {lift && <div className="nav-drag-ghost" style={{ left: lift.x + 12, top: lift.y + 8 }} aria-hidden>{splitTickets(lift.node.title).text}</div>}
    </nav>
  )
}

// age is how long a state has lasted, from when cmdrd says it began.
function age(since: number, nowSec: number): string {
  return since ? fmtAge(nowSec - since) : ''
}

function selectedItemKey(needs: { project: string; bead_id: string }[], sel: Selection): string | undefined {
  if (sel.kind !== 'ask') return undefined
  const a = needs.find((n) => askKey(n) === sel.key)
  return a ? itemKey(a) : undefined
}

// AskRows is the plain NEEDS YOU list, shown until the API config is known.
function AskRows({ vm, selKey, cursor, fresh }: { vm: Display; selKey: string; cursor: string; fresh: Record<string, true> }) {
  return (
    <>
      <SectionLabel title="Needs you" count={vm.asks.length} tone="attn" />
      {vm.asks.map((a) => (
        <button
          key={a.key}
          className={rowClass(a.key, selKey, cursor) + ' ask'}
          data-cursor={cursor === a.key}
          onClick={() => useStore.getState().select({ kind: 'ask', key: a.key })}
          title={`${a.text}\n${a.workstream}`}
        >
          <span className={`mark mark-needs${fresh[a.key] ? ' pulse' : ''}`}>⚑</span>
          <AskText text={a.stale_why ? `${a.text} · ${a.stale_why}` : a.text} />
          <span className="rank-tag">#{a.rank}</span>
        </button>
      ))}
    </>
  )
}

function rowClass(key: string, selKey: string, cursor: string): string {
  return `row${key === selKey ? ' selected' : ''}${key === cursor ? ' cursor' : ''}`
}

function SectionLabel({ title, count, tone }: { title: string; count?: number; tone?: 'attn' }) {
  return (
    <div className={`section${tone ? ` section-${tone}` : ''}`}>
      <span>{title}</span>
      {count !== undefined && <span className="section-count">{count}</span>}
    </div>
  )
}

function Workstream({ w, tracks, folded, onToggle, selected, cursor, mod }: { w: BoardRow; tracks: BoardTrack[]; folded: boolean; onToggle: () => void; selected: boolean; cursor: boolean; mod: string }) {
  const leaving = useStore((s) => !!s.leaving[w.key])
  const { lifted, showOwner } = useContext(NavContext)
  const progress = w.milestones ?? w.progress
  const progressLabel = `${progress.done}/${progress.total} ${w.milestones ? 'stages' : 'tasks'} completed`
  return (
    <div className={`ws${w.tracks.length ? ' has-tracks' : ''}${w.kind === 'quiet' ? ' can-complete' : ''}${leaving ? ' is-leaving' : ''}${lifted === w.key ? ' is-lifted' : ''}`}>
      {w.tracks.length > 0 && (
        <button
          type="button"
          className={`ws-fold${folded ? ' folded' : ''}`}
          aria-expanded={!folded}
          aria-label={`${folded ? 'Show' : 'Hide'} ${w.tracks.length} workstreams under ${w.title}`}
          title={`${w.tracks.length} workstream${w.tracks.length === 1 ? '' : 's'}`}
          onClick={onToggle}
        >
          <ChevronDown size={14} strokeWidth={2} aria-hidden />
        </button>
      )}
      <button
        className={`row${selected ? ' selected' : ''}${cursor ? ' cursor' : ''}`}
        data-cursor={cursor}
        data-nav-key={w.key}
        onClick={() => useStore.getState().openWorkflow(w.key, w.scope)}
        title={w.n <= 9 ? `${mod}${w.n}` : undefined}
      >
        <div className="row-body">
          <span className="ws-ring" aria-label={progressLabel} title={progressLabel}>
            {/* 0/0 is nothing planned yet, not finished work ("Not started: Manager to plan"). */}
            <ProgressRing pct={progress.total ? percent(progress) : 0} />
            <span className="rank">{w.n}</span>
          </span>
          <div className="row-text">
            <div className="row-head">
              <span className="row-title">
                {w.starred && <span className="star">★ </span>}
                {splitTickets(w.title).text}
              </span>
              {showOwner && <PlanningOwner owner={w.planning_commander} />}
              {w.why && <span className="ws-state-why" title={w.why} aria-label={w.why}>⚠</span>}
            </div>
            <WorkstreamState w={w} />
          </div>
        </div>
      </button>
      {w.kind === 'quiet' && <CompleteButton project={w.project} bead={w.bead_id} title={w.title} />}
      {!folded && tracks.length > 0 && (
        <div className="ws-tracks" role="group" aria-label={`Workstreams under ${w.title}`}>
          {tracks.map((t) => (
            <TrackRow key={t.key} t={t} n={w.n} wsKey={w.key} parentOwner={w.planning_commander} />
          ))}
        </div>
      )}
    </div>
  )
}

function PlanningOwner({ owner }: { owner?: string }) {
  if (!owner) return null
  return <span className="row-commander" title={owner} aria-label={`Responsible Commander: ${owner}`}>{commanderName(owner)}</span>
}

// ProgressRing draws a workstream's progress around its rank, so the row needs no bar of its own.
function ProgressRing({ pct }: { pct: number }) {
  return (
    <svg className="ws-ring-svg" width="26" height="26" viewBox="0 0 26 26" fill="none" aria-hidden>
      <circle className="ws-ring-track" cx="13" cy="13" r="11.5" />
      {pct > 0 && <circle className="ws-ring-done" cx="13" cy="13" r="11.5" pathLength={100} strokeDasharray={`${pct} 100`} />}
    </svg>
  )
}

// TrackRow is a nested workstream under its parent: number, title and the same one
// state line. A click opens the top-level workstream (one Manager runs all of it)
// scoped to this one. The sidebar shows two levels; a chevron opens what is deeper.
function TrackRow({ t, n, wsKey, parentOwner }: { t: BoardTrack; n: number; wsKey: string; parentOwner?: string }) {
  const leaving = useStore((s) => !!s.leaving[t.key])
  const nowSec = useStore((s) => s.nowSec)
  const { lifted, openNested, isOpen, toggleOpen } = useContext(NavContext)
  const open = isOpen(t.key)
  const project = t.key.slice(0, t.key.length - t.bead_id.length - 1)
  const children = t.children ?? []
  return (
    <div className={`track-wrap${t.kind === 'quiet' ? ' can-complete' : ''}${leaving ? ' is-leaving' : ''}${lifted === t.key ? ' is-lifted' : ''}`}>
      <button className={`row track-row${openNested === t.bead_id ? ' selected' : ''}`} aria-current={openNested === t.bead_id || undefined} data-nav-key={t.key} onClick={() => useStore.getState().openWorkflow(wsKey, t.bead_id)} title={t.title}>
        <div className="row-body">
          <div className="row-head">
            <span className="rank track-rank">{n}{t.letter}</span>
            <span className="row-title">{splitTickets(t.title).text}</span>
            {t.planning_commander !== parentOwner && <PlanningOwner owner={t.planning_commander} />}
          </div>
          <StateLine kind={t.kind} text={t.text} agents={t.working} age={age(t.since, nowSec)}
            action={t.kind === 'done' ? <CompleteButton project={project} bead={t.bead_id} title={t.title} /> : undefined} />
          {t.why && <span className="ws-state-why" title={t.why} aria-label={t.why}>⚠</span>}
        </div>
      </button>
      {t.kind === 'quiet' && <CompleteButton project={project} bead={t.bead_id} title={t.title} />}
      {children.length > 0 && (
        <button type="button" className={`track-fold${open ? '' : ' folded'}`} aria-expanded={open} aria-label={`${open ? 'Hide' : 'Show'} ${children.length} workstreams under ${t.title}`} onClick={() => toggleOpen(t.key)}>
          <ChevronDown size={13} strokeWidth={2} aria-hidden />
        </button>
      )}
      {open && children.length > 0 && (
        <div className="ws-tracks ws-tracks-nested" role="group" aria-label={`Workstreams under ${t.title}`}>
          {children.map((c) => <TrackRow key={c.key} t={c} n={n} wsKey={wsKey} parentOwner={t.planning_commander ?? parentOwner} />)}
        </div>
      )}
    </div>
  )
}

// CompleteButton closes a workstream the user has finished. It shows
// only when nothing under it is open, and only this click closes it; the row
// reads Completed at once, before the board catches up.
export function CompleteButton({ project, bead, title }: { project: string; bead: string; title: string }) {
  const apis = useApis()
  const key = `${project}:${bead}`
  const error = useStore((s) => s.completeError[key] ?? '')
  // The row leaves at once; the close (a br write, which can queue for a minute
  // behind the agents' writes) finishes in the background. A close that fails
  // brings the row back, red, with why.
  const complete = (at: { x: number; y: number }) => {
    if (!apis) return
    confetti(at.x, at.y)
    useStore.getState().markCompleted(key)
    void (async () => {
      let failed = ''
      try {
        const r = await apis.cmdr.runCmd(['internal-close-objective', '--project', project, '--bead', bead])
        failed = cmdError(r.stdout) || cmdError(r.stderr) || (r.ok ? '' : r.error || `exit ${r.exit}`)
      } catch (e) {
        failed = `could not reach Commander: ${(e as Error).message ?? e}`
      }
      // Closed already (an earlier click that got through) is done, not a failure.
      if (failed && !/already closed/i.test(failed)) useStore.getState().failCompleted(key, failed)
    })()
  }
  return (
    <button type="button" className={`complete-btn${error ? ' is-error' : ''}`}
      // It can sit inside its row (a finished row's state): the click is the
      // Complete, not a select or a drag of the row.
      onPointerDown={(e) => e.stopPropagation()}
      onClick={(e) => {
        e.stopPropagation()
        const r = e.currentTarget.getBoundingClientRect()
        complete({ x: r.left + r.width / 2, y: r.top + r.height / 2 })
      }} title={error ? `Could not close it: ${error}` : `Close "${title}": nothing under it is open`}>
      {error ? 'Retry Complete' : '✓ Complete'}
    </button>
  )
}

// cmdError is the error a cmdr command printed as JSON ({"error": ...}), or "".
function cmdError(out: string): string {
  for (const line of out.split('\n')) {
    try {
      const v = JSON.parse(line) as { error?: string }
      if (v?.error) return v.error
    } catch {
      // not a JSON line
    }
  }
  return ''
}

// realParent is a nested bead's parent in the plan.
function realParent(snapshot: Snapshot | null, bead: string): string | undefined {
  const find = (steps: SnapStep[] | undefined, up: string): string | undefined => {
    for (const s of steps ?? []) {
      if (s.bead_id === bead) return up
      const hit = find(s.children, s.bead_id)
      if (hit) return hit
    }
    return undefined
  }
  for (const w of snapshot?.workstreams ?? []) {
    const hit = find(w.steps, w.bead_id)
    if (hit) return hit
  }
  return undefined
}

// cmdWarnings are what a command did only partly, from its JSON result.
function cmdWarnings(out: string): string[] {
  for (const line of out.split('\n')) {
    try {
      const v = JSON.parse(line) as { warnings?: string[] }
      if (v?.warnings?.length) return v.warnings
    } catch {
      // not a JSON line
    }
  }
  return []
}

// Operators is the YOUR AGENTS Operators tier: every Operator with its task,
// worktree, state and cost. A click opens its chat (live when it runs
// natively, else a read-only card) in the middle column.
export function Operators() {
  const snapshot = useStore((s) => s.snapshot)
  const chats = useStore((s) => s.chats)
  const selected = useStore((s) => s.selected)
  const nowSec = useStore((s) => s.nowSec)
  const ops = useMemo(() => (snapshot ? operatorsVM(snapshot, chats) : []), [snapshot, chats])
  const finished = useMemo(
    () => (snapshot ? finishedOperatorsVM(snapshot, nowSec).filter((f) => !ops.some((o) => o.role === f.role)) : []),
    [snapshot, nowSec, ops],
  )
  return (
    <>
      <TierLabel title="Operators" count={ops.length} />
      {ops.length === 0 && finished.length === 0 && <div className="tier-empty">No Operators running</div>}
      <div className="ops" aria-label="Operators">
        {ops.map((o) => {
          const on = selected.kind === 'agent' && selected.key === o.role
          return (
            <button
              key={o.role}
              className={`op op-${o.state}${on ? ' selected' : ''}`}
              title={`${o.role}\n${o.wsTitle}\n${o.native ? 'native chat' : 'runs in the tmux cockpit (read-only here)'}`}
              onClick={() => useStore.getState().select({ kind: 'agent', key: o.role, ws: o.wsKey })}
            >
              <span className="op-line">
                <span className="op-dot" />
                <span className="op-name">{o.label}</span>
                <span className="op-state">{o.state}</span>
                {o.cost !== undefined && <span className="op-cost">{fmtUSD(o.cost)}</span>}
                {o.native && <span className="tag-native">native</span>}
              </span>
              <span className="op-sub">
                <span className="op-task">{o.task}</span>
                {o.branch && <span className="op-branch">⎇ {o.branch}</span>}
              </span>
            </button>
          )
        })}
        {finished.map((f) => {
          const on = selected.kind === 'agent' && selected.key === f.role
          return (
            <button
              key={`finished-${f.role}`}
              className={`op op-finished${f.died ? ' op-stuck' : ''}${on ? ' selected' : ''}`}
              title={`${f.role}\n${f.wsTitle}`}
              onClick={() => useStore.getState().select({ kind: 'agent', key: f.role, ws: f.wsKey })}
            >
              <span className="op-line">
                <span className="op-dot" />
                <span className="op-name">{f.label}</span>
                <span className="op-state">{f.state}</span>
              </span>
              {f.report && (
                <span className="op-sub">
                  <span className="op-task">{f.report}</span>
                </span>
              )}
            </button>
          )
        })}
      </div>
    </>
  )
}


// YourAgents heads the agent tiers (it was the FLEET footer): spend and counts.

/**
 * WorkstreamState is a workstream's state, first-class: what it is doing (in
 * its state's colour), then how many agents are working on it right now and
 * how long it has been in this state.
 */
function WorkstreamState({ w }: { w: BoardRow }) {
  const nowSec = useStore((s) => s.nowSec)
  return (
    <>
      <StateLine
        kind={w.kind}
        text={w.text}
        title={w.gate?.at && !w.gate.for_you ? `${w.text}: ${w.gate.who}` : w.text}
        agents={w.working}
        age={age(w.since, nowSec)}
        action={w.kind === 'done' ? <CompleteButton project={w.project} bead={w.bead_id} title={w.title} /> : undefined}
      />
    </>
  )
}

/**
 * StateLine is the one meta line under a sidebar row (Commander and every
 * workstream): the status text, in its state's colour, on the left, then the
 * agents pill and the time pill on the right. The text wraps, the pills stay.
 */
// action replaces the text and pills: a finished row's state is its Complete button.
function StateLine({ kind, text, title, agents, age, unread = 0, action }: { kind: string; text: string; title?: string; agents: number; age: string; unread?: number; action?: ReactNode }) {
  if (action) return <div className={`ws-state agent-${kind}`}>{action}</div>
  return (
    <div className={`ws-state agent-${kind}`}>
      {text && (
        <span className="ws-state-text" title={title ?? text}>
          {text}
        </span>
      )}
      <span className="ws-state-meta">
        {agents > 0 && (
          <span className="ws-state-agents">
            <Spinner size={9} /> {agents}
          </span>
        )}
        {age && <span className="ws-state-age">{age}</span>}
        {unread > 0 && (
          <span className="unread" title={`${unread} unread`}>
            ✉ {unread}
          </span>
        )}
      </span>
    </div>
  )
}

export function YourAgents({ f }: { f: FleetVM }) {
  return (
    <div className="agents-head">
      <div className="section section-agents">
        <span>Your agents</span>
        <span className="section-count">{f.agents}</span>
      </div>
      <div className="agents-sum">
        <span title={f.breakdown || undefined}>
          <b>{f.today}</b> est. today · {f.week} 7d
        </span>
        <span className="tone-run">
          <b>{f.active}</b> active
        </span>
        <span className={f.blocked > 0 ? 'tone-err' : ''}>
          <b>{f.blocked}</b> blocked
        </span>
      </div>
    </div>
  )
}

function TierLabel({ title, count }: { title: string; count?: number }) {
  return (
    <div className="tier">
      <span>{title}</span>
      {count !== undefined && <span className="section-count">{count}</span>}
    </div>
  )
}

function NavSkeleton() {
  return (
    <nav className="nav" aria-label="Navigator" aria-busy>
      <div className="nav-scroll">
        <div className="section">
          <span>Your agents</span>
        </div>
        {[0, 1, 2, 3, 4].map((i) => (
          <div key={i} className="skeleton-row">
            <span />
            <span />
          </div>
        ))}
      </div>
    </nav>
  )
}

function AskText({ text }: { text: string }) {
  const t = splitTickets(text)
  return (
    <>
      <span className="ask-text">{t.text}</span>
    </>
  )
}
