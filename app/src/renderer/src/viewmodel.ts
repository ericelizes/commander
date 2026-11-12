import { normalizeOwnerSnapshot } from './ownerCompat'
// Pure snapshot -> view model functions. The board's rows are cmdrd's
// (snapshot.display, livedata.Board); these shape the rest of the screen.

import { splitTickets } from '../../features/linear/Ticket'
import { useIntegrations } from '../../features/linear/settings'
import type { BoardRow, BoardTrack, Display, SnapAgent, SnapFleet, SnapStep, SnapWorkstream, Snapshot } from '../../shared/snapshot'

export type Tone = 'attn' | 'run' | 'ok' | 'err' | 'muted' | 'accent'

export interface AgentLine {
  kind: 'none' | 'idle' | 'working' | 'waiting' | 'stuck' | 'needs' | 'stalled' | 'quiet' | 'done'
  text: string
  age: string
  since?: number // unix s the state began, when cmdrd knows it (a recorded wait, a Manager's turn)
}

/** SLOW_CI_SECS: a quiet Operator or CI running this long is worth a look (the chat watcher marks it slow). */
export const SLOW_CI_SECS = 30 * 60

export type Marker = 'spin' | 'run' | 'needs' | 'blocked' | 'done' | 'review' | 'open'

export interface StepRow {
  key: string
  kind: 'step' | 'more' | 'done'
  depth: number
  text: string
  note?: string // muted suffix: a collapsed phase's state
  pr?: string // "#4107" while in review
  owner?: string
  starred?: boolean
  marker?: Marker
  now?: boolean
}

/** clock is a unix time as the local clock reads it: "21:45", with the day when it is not today. */
export function clock(unix: number, now = new Date()): string {
  const d = new Date(unix * 1000)
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toDateString() === now.toDateString() ? time : `${d.toLocaleDateString([], { weekday: 'short' })} ${time}`
}

export interface FleetVM {
  today: string
  week: string
  series: number[]
  /** Hover text for the spend line: where the 24h figure came from (est.). */
  breakdown: string
  agents: number
  active: number
  blocked: number
}

export function wsKey(ws: { project: string; bead_id: string }): string {
  return `${ws.project}:${ws.bead_id}`
}

export function askKey(a: { project: string; bead_id: string }): string {
  return `ask:${a.project}:${a.bead_id}`
}

/** fmtAge is a compact duration: 40s, 14m, 3h, 2d. */
export function fmtAge(secs: number): string {
  if (secs < 60) return `${Math.max(Math.floor(secs), 0)}s`
  if (secs < 3600) return `${Math.floor(secs / 60)}m`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h`
  return `${Math.floor(secs / 86400)}d`
}

// plainTitle is a step's title without its ticket id (the pill shows it):
// "(KEY-123)" for the configured Linear team key only, so "(UTF-8)" or
// "(SHA-256)" stays. Mirrors livedata.plainTitle.
export function plainTitle(t: string, team = useIntegrations.getState().linearTeam): string {
  const key = team.trim().toUpperCase()
  if (!/^[A-Z][A-Z0-9]{0,5}$/.test(key)) return t.trim()
  return t.replace(new RegExp(`\\s*\\(${key}-\\d{1,6}\\)\\s*`, 'g'), ' ').trim()
}

// ---- approval gates -----------------------------------------------------------
//
// A workstream's status is its next approval gate and how close it is: the
// units of work (leaf beads) the gate waits on, how many are done, and who is
// on them. A gate waits on the work before it (its blocked_by, followed
// through containers to their leaves and through other work, stopping at an
// approved gate); the work after it waits on it.

export interface GateStatus {
  gate: SnapStep // the next gate not yet approved
  at: boolean // everything it waits on is done: only the approval is left
  done: number
  total: number
  operators: number // Operators with a live process on its unfinished units
  operatorRoles: string[] // everyone who claimed them, live or not (a claim never disappears)
  waits: string[] // recorded waits on the unfinished work before it: "CI #104", "review (reviewer-a)", "you"
  gateWait?: string // what the gate itself waits on: "review (reviewer-a, reviewer-b)"
  waitBeads: SnapStep[] // the unfinished beads those waits are on (for when each began)
}

/** approverNames is who approves, as the user reads it ("you" for user). */
export function approverNames(a: string[] | undefined): string {
  return (a ?? []).map((x) => (x === 'user' ? 'you' : x)).join(', ')
}

/** parked: `br defer`red by the user, out of play (no next gate, wait, stall or progress) until undeferred. */
export const parked = (s: SnapStep) => s.status === 'deferred'

/** onUser: an open, unparked bead that waits on the user (needs-user, legacy marker normalized by cmdrd, waiting: user). */
const onUser = (s: SnapStep) => !parked(s) && s.status !== 'closed' && s.state !== 'done' && (s.state === 'needs-user' || s.waiting === 'you')

export function gateStatus(ws: SnapWorkstream): GateStatus | null {
  const byId = new Map<string, SnapStep>()
  const index = (steps: SnapStep[]) => steps.forEach((s) => !parked(s) && (byId.set(s.bead_id, s), index(s.children ?? [])))
  index(ws.steps)
  const closed = (s: SnapStep) => s.status === 'closed' || s.state === 'done'
  const gates = [...byId.values()].filter((s) => s.gate)
  if (!gates.length) return null
  const leavesOf = (s: SnapStep): SnapStep[] => workUnits(s)
  // units a gate waits on, and whether it waits on another gate (for order)
  const before = (g: SnapStep) => {
    const units = new Map<string, SnapStep>()
    const waits: string[] = []
    const waitBeads: SnapStep[] = []
    const seen = new Set<string>()
    let gatesBefore = 0
    const visit = (id: string) => {
      if (seen.has(id)) return
      seen.add(id)
      const s = byId.get(id)
      if (!s) return
      if (s.gate && s !== g) {
        gatesBefore++
        if (closed(s)) return // an approved gate closes the stretch before it
      }
      if (!s.gate && !closed(s) && s.waiting) {
        waits.push(...s.waiting.split(' · '))
        waitBeads.push(s)
      }
      for (const leaf of leavesOf(s)) if (!leaf.gate) units.set(leaf.bead_id, leaf)
      for (const leaf of s.gate ? [] : leavesOf(s)) for (const d of leaf.blocked_by ?? []) visit(d)
      for (const d of s.blocked_by ?? []) visit(d)
    }
    for (const d of g.blocked_by ?? []) visit(d)
    return { units: [...units.values()], gatesBefore, waits, waitBeads }
  }
  // Earliest open stage first. A gate that waits on nothing (the workflow
  // gate) only blocks the work after it, so it never takes over from a stage
  // that is waiting on its own work or reviewers.
  const stage = (g: SnapStep) => ((g.blocked_by ?? []).length ? 0 : 1)
  const order = gates
    .map((g) => ({ g, ...before(g) }))
    .sort((a, b) => Number(closed(a.g)) - Number(closed(b.g)) || Number(onUser(b.g)) - Number(onUser(a.g)) || a.gatesBefore - b.gatesBefore || stage(a.g) - stage(b.g))
  const next = order.find((o) => !closed(o.g))
  if (!next) return null
  // A gate with no recorded "before" waits on the workstream's other work.
  const units = next.units.length ? next.units : [...byId.values()].filter((s) => !s.gate && !s.children?.length)
  const done = units.filter(closed).length
  const claimed = units.filter((u) => !closed(u) && u.owner?.startsWith('operator-'))
  const operatorRoles = [...new Set(claimed.map((u) => u.owner!))]
  const operators = new Set(claimed.filter(alive).map((u) => u.owner!)).size
  return { gate: next.g, at: done === units.length, done, total: units.length, operators, operatorRoles, gateWait: next.g.waiting && next.g.waiting !== 'you' ? next.g.waiting : undefined, waits: [...new Set(next.waits)], waitBeads: next.waitBeads }
}

// alive: a claim's process decorates it. Without owner_state (an older cmdrd)
// the claim is taken as live, as it was then.
const alive = (s: SnapStep) => s.owner_state === undefined || s.owner_state === 'working'

/**
 * workUnits are the beads that count as work under s: its leaves. A stage whose
 * sub-tasks are all closed but which is itself still open is a unit of its own
 * (its sub-tasks are progress, only closing the stage finishes it).
 */
export function workUnits(s: SnapStep): SnapStep[] {
  if (parked(s)) return []
  const kids = (s.children ?? []).filter((c) => !parked(c))
  if (!kids.length) return [s]
  const units = kids.flatMap(workUnits)
  if (!s.gate && s.status !== 'closed' && units.every((u) => u.status === 'closed' || u.state === 'done')) return [s]
  return units
}

/** progress is a workstream's units of work (leaf beads, not gates), done in aggregate. */
export function progress(ws: SnapWorkstream): { done: number; total: number } {
  const units: SnapStep[] = []
  ws.steps.forEach((x) => units.push(...workUnits(x).filter((u) => !u.gate)))
  return { done: units.filter((u) => u.status === 'closed' || u.state === 'done').length, total: units.length }
}

/** percent is how far along the work before the next gate is. */
export function percent(g: { done: number; total: number }): number {
  return g.total ? Math.round((100 * g.done) / g.total) : 100
}

/**
 * deriveStep mirrors livedata.DerivedLive: a row shows what is under it. Done
 * only when its own bead is closed (never from its children); needs-user/blocked on the row itself win; else in
 * progress when a descendant is in progress, in review or owned by a live
 * agent; else blocked, else not started. `working` is true when an agent under
 * it is working. Idempotent, so it agrees with a newer cmdrd.
 */
export function deriveStep(s: SnapStep, agents: Record<string, SnapAgent> = {}): SnapStep {
  const allKids = (s.children ?? []).map((c) => deriveStep(c, agents))
  const kids = allKids.filter((k) => !parked(k)) // a parked child is out of play
  const agent = s.owner ? agents[s.owner]?.state : undefined
  const working = agent === 'working' || kids.some((k) => k.working)
  const base = { ...s, children: s.children ? allKids : undefined }
  if (s.status === 'closed') return { ...base, state: 'done', working: false }
  if (parked(s)) return { ...base, state: 'not-started', working: false }
  if (s.state === 'needs-user' || s.state === 'blocked') return { ...base, working }
  if (!kids.length) {
    // A claim (owner on an open step) is someone on it, process or not.
    return { ...base, state: (agent || s.owner) && s.state === 'not-started' ? 'in-progress' : s.state, working }
  }
  const seen = new Set(kids.map((k) => (isDone(k) ? 'done' : k.state)))
  let state = 'not-started'
  // An open gate is not approved by its children being done.
  // Nor does an open stage become done: its sub-tasks are progress, only its
  // own bead closing finishes it (started, though).
  if (kids.every(isDone)) state = s.gate ? s.state : s.state === 'not-started' || s.state === 'done' ? 'in-progress' : s.state
  else if (seen.has('needs-user')) state = 'needs-user'
  else if (seen.has('in-progress') || seen.has('review') || working) state = 'in-progress'
  else if (seen.has('blocked')) state = 'blocked'
  // Some of it done and the rest still to do: started, not "not started".
  else if (seen.has('done')) state = 'in-progress'
  return { ...base, state, working }
}

/** deriveSnapshot applies deriveStep to every workstream so all views agree. */
export function deriveSnapshot(s: Snapshot): Snapshot {
 s = normalizeOwnerSnapshot(s)
  const agents = s.agents ?? {}
  const executionFacts = s.storage === 'postgres' && s.executions != null ? s.executions : undefined
  const observedStep = (step: SnapStep, projectID: number | undefined): SnapStep => {
    const children = (step.children ?? []).map((child) => observedStep(child, projectID))
    const own = executionFacts?.some((run) => !!run.run_id && run.project_id === projectID && run.bead_id === step.bead_id && run.state === 'working') ?? false
    return { ...step, children: step.children ? children : undefined, working: own || children.some((child) => child.working) }
  }
  const workstreams = (s.workstreams ?? []).map((ws) => {
    const root = deriveStep({ ...(ws as unknown as SnapStep), children: ws.steps ?? [] }, agents)
    const observed = executionFacts ? observedStep(root, ws.project_id) : root
    const working = executionFacts ? !!observed.working : !!root.working || agents[ws.manager_role]?.state === 'working'
    return { ...ws, state: root.state, working, steps: observed.children ?? [] }
  })
  const warnings = new Map(s.display?.asks.map((a) => [a.key, a.stale_why]))
  return { ...s, workstreams, needs_you: (s.needs_you ?? []).map((a) => ({ ...a, stale_why: warnings.get(askKey(a)) })) }
}

const EMPTY_BOARD: Display = { commander: { kind: 'none', text: 'no agent yet', since: 0, unread: 0 }, asks: [], workstreams: [] }

/**
 * resolveClocks turns the clock marks cmdrd puts in board text ("@1761690000";
 * the daemon may run in UTC, so it never formats a time) into the viewer's local
 * time: "09:45 PM", with the weekday when it is not today. Mirrors
 * livedata.ResolveClocks.
 */
export function resolveClocks(text: string, now: Date = new Date()): string {
  return text.replace(/@(\d{9,11})\b/g, (_m, u: string) => {
    const d = new Date(Number(u) * 1000)
    const t = d.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })
    return d.toDateString() === now.toDateString() ? t : `${d.toLocaleDateString('en-US', { weekday: 'short' })} ${t}`
  })
}

function resolveDeep<T>(v: T, now: Date): T {
  if (typeof v === 'string') return (v.includes('@') ? resolveClocks(v, now) : v) as T
  if (Array.isArray(v)) return v.map((x) => resolveDeep(x, now)) as T
  if (v && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, resolveDeep(x, now)])) as T
  return v
}

/** withLocalClocks is s with every clock mark in its display block in local time. */
export function withLocalClocks(s: Snapshot, now: Date = new Date()): Snapshot {
  return s.display ? { ...s, display: resolveDeep(s.display, now) } : s
}

/** board is the snapshot's board as cmdrd derived it (empty from a cmdrd that predates it). */
export function board(s: Snapshot | null | undefined): Display {
  return s?.display ?? EMPTY_BOARD
}

export function isDone(s: SnapStep): boolean {
  return s.state === 'done' || s.status === 'closed'
}

/** isActive: the step, or anything under it, is moving or needs attention. */
export function isActive(s: SnapStep): boolean {
  if (!isDone(s) && (s.state === 'in-progress' || s.state === 'needs-user' || s.state === 'blocked')) return true
  return (s.children ?? []).some(isActive)
}

function contains(s: SnapStep, id: string): boolean {
  return (s.children ?? []).some((c) => c.bead_id === id || contains(c, id))
}

/**
 * managerOfRole finds who a Manager role belongs to: a workstream's own
 * Manager, or a nested workstream with a Manager of its own (manager_role on
 * the step, cmdrd with nested Managers on). Undefined for any other role.
 */
export function managerOfRole(s: Snapshot | null | undefined, role: string): { ws: SnapWorkstream; step?: SnapStep; title: string } | undefined {
  for (const ws of s?.workstreams ?? []) {
    if (ws.manager_role === role) return { ws, title: ws.title }
  }
  for (const ws of s?.workstreams ?? []) {
    const step = flatten(ws.steps ?? []).find((st) => st.manager_role === role)
    if (step) return { ws, step, title: `${ws.title} › ${step.title}` }
  }
  return undefined
}

function flatten(steps: SnapStep[]): SnapStep[] {
  return steps.flatMap((s) => [s, ...flatten(s.children ?? [])])
}

/** stepCounts tallies a workstream's steps (phases included, as the TUI does). */
export function stepCounts(ws: SnapWorkstream) {
  const c = { needs: 0, running: 0, blocked: 0, open: 0, done: 0 }
  for (const s of flatten(ws.steps ?? [])) {
    if (isDone(s)) c.done++
    else if (s.state === 'needs-user') c.needs++
    else if (s.state === 'blocked') c.blocked++
    else if (s.state === 'in-progress') c.running++
    else c.open++
  }
  return c
}

const stepLabelRe = /^([A-Za-z]+)(\d+)\b/

function truncate(s: string, n: number): string {
  const chars = [...s]
  return chars.length <= n ? s : chars.slice(0, n - 1).join('') + '…'
}

/** hiddenLabels names hidden steps: "U2, U4, U6–U9" when titles lead with a code, else short titles. */
export function hiddenLabels(steps: SnapStep[]): string {
  const codes: { prefix: string; n: number }[] = []
  const names: string[] = []
  for (const s of steps) {
    const m = stepLabelRe.exec(s.title.trim())
    if (m) codes.push({ prefix: m[1], n: Number(m[2]) })
    else names.push(truncate(s.title, 14))
  }
  const parts: string[] = []
  for (let i = 0; i < codes.length; ) {
    let j = i
    while (j + 1 < codes.length && codes[j + 1].prefix === codes[i].prefix && codes[j + 1].n === codes[j].n + 1) j++
    if (j - i >= 2) parts.push(`${codes[i].prefix}${codes[i].n}–${codes[j].prefix}${codes[j].n}`)
    else for (let k = i; k <= j; k++) parts.push(`${codes[k].prefix}${codes[k].n}`)
    i = j + 1
  }
  parts.push(...names)
  if (parts.length > 6) return [...parts.slice(0, 6), '…'].join(', ')
  return parts.join(', ')
}

/** maxVisible is how many open steps one level shows before "+N more". */
export const maxVisible = 6

function stepMarker(s: SnapStep, spinning: boolean): Marker {
  const state = isDone(s) ? 'done' : s.state
  if (s.review && state === 'in-progress') return 'review'
  switch (state) {
    case 'in-progress':
      return spinning ? 'spin' : 'run'
    case 'needs-user':
      return 'needs'
    case 'blocked':
      return 'blocked'
    case 'done':
      return 'done'
  }
  return 'open'
}

/**
 * stepRows lays a workstream's steps out as a tree in plan order: the active
 * phase expanded, other phases one line with their state, done steps a
 * "✓ n done" under their parent, and "+N more · U2, U4" naming what is hidden.
 * working: the Manager is working right now, so the now-step spins.
 */
export function stepRows(ws: SnapWorkstream, working: boolean): StepRow[] {
  const now = ws.now_step ?? ''
  const row = (s: SnapStep, depth: number, note?: string): StepRow => {
    const pr = s.review && !isDone(s) ? s.review.replace(/^\?/, '') : ''
    const isNow = s.bead_id !== '' && s.bead_id === now
    return {
      key: s.bead_id || s.title,
      kind: 'step',
      depth,
      text: s.state === 'needs-user' && s.ask ? s.ask : s.title,
      note,
      pr: pr ? `#${pr}` : undefined,
      // Every step is the Manager's by default, so only other owners (the user, Operators) are named.
      owner: s.owner && s.owner !== ws.owner && !s.owner.startsWith('manager-') ? s.owner : undefined,
      starred: s.starred,
      marker: stepMarker(s, (isNow && working && !isDone(s)) || !!s.working),
      now: isNow,
    }
  }
  const level = (nodes: SnapStep[], depth: number, parentKey: string): StepRow[] => {
    const open = nodes.filter((n) => !isDone(n))
    const done = nodes.filter(isDone)
    const isPhase = (n: SnapStep) => (n.children?.length ?? 0) > 0
    // The phase holding the now-step opens first, else the first moving phase,
    // else the first open one, so the dropdown always shows where to look next.
    let activePhase = open.findIndex((n) => isPhase(n) && now !== '' && contains(n, now))
    if (activePhase < 0) activePhase = open.findIndex((n) => isPhase(n) && isActive(n))
    if (activePhase < 0) activePhase = open.findIndex(isPhase)
    const visible = open.map((n, i) => isActive(n) || n.bead_id === now || i === activePhase)
    let shown = visible.filter(Boolean).length
    for (let i = 0; i < open.length; i++) {
      if (!visible[i] && shown < maxVisible) {
        visible[i] = true
        shown++
      }
    }
    const rows: StepRow[] = []
    const hidden: SnapStep[] = []
    open.forEach((n, i) => {
      if (!visible[i]) {
        hidden.push(n)
        return
      }
      if (!isPhase(n)) rows.push(row(n, depth))
      else if (i === activePhase) {
        rows.push(row(n, depth))
        rows.push(...level(n.children ?? [], depth + 1, n.bead_id))
      } else rows.push(row(n, depth, n.phase_note))
    })
    if (hidden.length > 0) {
      rows.push({ key: `more:${parentKey}`, kind: 'more', depth, text: `+${hidden.length} more · ${hiddenLabels(hidden)}` })
    }
    if (done.length > 0) {
      rows.push({ key: `done:${parentKey}`, kind: 'done', depth, text: `✓ ${done.length} done` })
    }
    return rows
  }
  return level(ws.steps ?? [], 0, ws.bead_id)
}

export function fmtUSD(v: number): string {
  if (!Number.isFinite(v) || v <= 0) return '$0'
  if (v < 10) return `$${v.toFixed(2)}`
  if (v < 1000) return `$${v.toFixed(0)}`
  return `$${(v / 1000).toFixed(1)}k`
}

function spendBreakdown(f: SnapFleet | undefined): string {
  if (!f) return ''
  const roles = f.roles_cost ?? []
  const chat = f.chat_cost_usd_24h ?? 0
  const managers = roles.filter((r) => r.role.startsWith('manager-')).reduce((n, r) => n + r.today_usd, 0)
  const commander = roles.filter((r) => r.role === 'commander').reduce((n, r) => n + r.today_usd, 0)
  const parts = [
    `Operators ${fmtUSD(Math.max(0, f.cost_usd_24h - chat))}`,
    `Commander ${fmtUSD(commander)}`,
    `Managers ${fmtUSD(managers)}`,
  ]
  return `Last 24h, estimated (plan-equivalent): ${parts.join(' · ')}`
}

/** roleCostNote is the chat header tooltip's spend line for a role, or undefined when nothing is recorded. */
export function roleCostNote(f: SnapFleet | undefined, role: string): string | undefined {
  const r = f?.roles_cost?.find((c) => c.role === role)
  if (!r || r.total_usd <= 0) return undefined
  return `est. ${fmtUSD(r.today_usd)} today, ${fmtUSD(r.total_usd)} total`
}

export function fleetVM(f: SnapFleet | undefined): FleetVM {
  return {
    breakdown: spendBreakdown(f),
    today: fmtUSD(f?.cost_usd_24h ?? 0),
    week: fmtUSD(f?.cost_usd_7d ?? 0),
    series: f?.cost_series_24h ?? [],
    agents: f?.agents ?? 0,
    active: f?.active ?? 0,
    blocked: f?.blocked ?? 0,
  }
}

/** sparkPoints maps a series onto a w x h box (SVG polyline points); a flat series sits on the baseline. */
export function sparkPoints(series: number[], w: number, h: number): string {
  if (series.length === 0) return ''
  const max = Math.max(...series, 0)
  const step = series.length > 1 ? w / (series.length - 1) : 0
  return series
    .map((v, i) => {
      const y = max > 0 ? h - (Math.max(v, 0) / max) * (h - 2) - 1 : h - 1
      return `${(i * step).toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

/** isTrack: a workstream nested under another one, a separate outcome. */
export const isTrack = (s: SnapStep) => !!s.track


/** trackTitle drops a letter an agent typed into the title ("A. Script audits"): the letter is built in. */
export function trackTitle(title: string): string {
  return title.replace(/^[A-Z][.):]\s+/, '')
}

const openTracksIn = (steps: SnapStep[] | undefined): SnapStep[] =>
  (steps ?? []).filter((s) => isTrack(s) && !s.superseded && s.status !== 'closed' && s.state !== 'done')

/** openTracks are the open workstreams directly under a workstream, in priority order. */
export function openTracks(ws: SnapWorkstream): SnapStep[] {
  return openTracksIn(ws.steps)
}

/** Visible nested workstreams, each before its children; active progress can exclude paused subtrees. */
export function openNested(ws: SnapWorkstream, includePaused = true): SnapStep[] {
  const walk = (steps: SnapStep[] | undefined): SnapStep[] => openTracksIn(steps).filter((s) => includePaused || !parked(s)).flatMap((s) => [s, ...walk(s.children)])
  return walk(ws.steps)
}

/** openLeaves are the open nested workstreams that hold steps, not more workstreams. */
export function openLeaves(ws: SnapWorkstream): SnapStep[] {
  return openNested(ws).filter((s) => openTracksIn(s.children).length === 0)
}

/** allTracks is every nested workstream a sidebar row shows, each before its children. */
export function allTracks(row: BoardRow | undefined): BoardTrack[] {
  const walk = (ts: BoardTrack[] | undefined): BoardTrack[] => (ts ?? []).flatMap((t) => [t, ...walk(t.children)])
  return walk(row?.tracks)
}

export function findTrack(row: BoardRow | undefined, id: string | undefined): BoardTrack | undefined {
  return id ? allTracks(row).find((t) => t.bead_id === id) : undefined
}

export type LinkTab = 'overview' | 'workflow' | 'decision'

// LinkTarget is where a link to a bead lands: its workstream, the nested workstream
// it sits in (when it does), and the decision waiting on that exact bead.
export interface LinkTarget {
  ws?: SnapWorkstream
  track?: string
  letter?: string
  ask?: Snapshot['needs_you'][number]
}

export function linkTarget(snapshot: Snapshot | null | undefined, id: string): LinkTarget | null {
  if (!snapshot) return null
  const ask = snapshot.needs_you.find((n) => n.bead_id === id)
  const within = (steps: SnapStep[], track?: string): { track?: string } | null => {
    for (const s of steps) {
      const here = s.track ? s.bead_id : track
      if (s.bead_id === id) return { track: here }
      const found = within(s.children ?? [], here)
      if (found) return found
    }
    return null
  }
  for (const ws of snapshot.workstreams) {
    const at = ws.bead_id === id ? {} : within(ws.steps ?? []) ?? (id.startsWith(`${ws.bead_id}.`) ? {} : null)
    if (!at) continue
    const row = snapshot.display?.workstreams.find((r) => r.key === wsKey(ws))
    return { ws, track: at.track, letter: findTrack(row, at.track)?.letter, ask }
  }
  const ws = ask && snapshot.workstreams.find((w) => w.project === ask.project && w.title === ask.workstream)
  return ask ? { ws: ws || undefined, track: ask.track_id || undefined, ask } : null
}

const TAB_NAMES: Record<LinkTab, string> = { overview: 'Overview', workflow: 'Workflow', decision: 'Decision' }

// linkLabel says in the user's terms where a link goes: "Workstream 1.2, Workflow".
export function linkLabel(snapshot: Snapshot | null | undefined, id: string, tab?: LinkTab): string {
  const t = linkTarget(snapshot, id)
  if (!t) return 'Not on the board right now'
  const shown = tab === 'workflow' ? 'workflow' : 'overview' // a decision reads on its Overview
  return [t.ws ? `Workstream ${t.ws.n ?? ''}${t.letter ?? ''}`.trim() : '', TAB_NAMES[shown]].filter(Boolean).join(', ')
}

/** navOrder is the keyboard order of selectable rows: Commander, asks, workstreams. */
// navOrder is the navigator's top-to-bottom order: NEEDS YOU, then YOUR
// AGENTS (Commander, then the workstream Managers).
export function navOrder(vm: Display): string[] {
  return [...vm.asks.map((a) => a.key), 'commander', ...vm.workstreams.map((w) => w.key)]
}

// agent: an Operator's chat (key = its role), under workstream ws.
export type Selection = { kind: 'commander' } | { kind: 'progress' } | { kind: 'ws'; key: string } | { kind: 'ask'; key: string } | { kind: 'agent'; key: string; ws: string }

/** homeActive: the Home page is on whenever Commander is the conversation, which is every selection but Progress and an agent's own chat. */
export function homeActive(sel: Selection): boolean {
  return sel.kind === 'commander' || sel.kind === 'ws' || sel.kind === 'ask'
}

export interface NavState {
  selected: Selection
}

// activateWorkstream selects a workstream (its Manager's chat and its graph)
// in one click; its steps are in the Graph tab, nothing expands in the sidebar.
export function activateWorkstream(st: NavState, key: string): NavState {
  return { ...st, selected: { kind: 'ws', key } }
}

/** shortcut handles Ctrl/Cmd+n: 0 is Commander, 1..9 the nth workstream. */
export function shortcut(st: NavState, vm: Display, n: number): NavState {
  if (n === 0) return { ...st, selected: { kind: 'commander' } }
  const ws = vm.workstreams[n - 1]
  return ws ? activateWorkstream(st, ws.key) : st
}

export function selectionKey(sel: Selection): string {
  return sel.kind === 'commander' || sel.kind === 'progress' ? 'commander' : sel.kind === 'agent' ? sel.ws : sel.key
}

/**
 * operatorRoles lists a workstream's Operators: the operator-* owners of its
 * unfinished steps, plus native chat roles named after its bead (Operator
 * roles embed it, e.g. operator-op-fix-x-example-app-t05-6-1).
 */
export function operatorRoles(ws: SnapWorkstream, chatRoles: string[] = []): string[] {
  const out = new Set<string>()
  const walk = (steps: SnapStep[]) => {
    for (const st of steps) {
      if (st.owner?.startsWith('operator-') && st.state !== 'done' && st.status !== 'closed') out.add(st.owner)
      if (st.children) walk(st.children)
    }
  }
  walk(ws.steps ?? [])
  for (const r of chatRoles) if (r.startsWith('operator-') && r.includes(ws.bead_id)) out.add(r)
  return [...out]
}

/** operatorLabel shortens operator-op-fix-assistant-sh-example-app-t05-6-1 to "fix-assistant-sh · 6-1". */
export function operatorLabel(role: string, beadId: string): string {
  let s = role.replace(/^operator-/, '').replace(/^op-/, '')
  const i = s.indexOf(`-${beadId}-`)
  if (i >= 0) return `${s.slice(0, i)} · ${s.slice(i + beadId.length + 2)}`
  return s
}

export interface OperatorVM {
  role: string
  wsKey: string
  wsTitle: string
  label: string // short name, e.g. "fix-assistant-sh · 6-1"
  task: string // the step it owns, else its workstream
  branch?: string // its worktree dir (cmdrd chat status cwd), when known
  state: string // native chat state, else the hook/scraped state, else "tmux"
  native: boolean
  cost?: number // native sessions only
}

/** The chat fields operatorsVM reads (a subset of chatApi's ChatStatus). */
export interface OperatorChat {
  running: boolean
  state: string
  total_cost_usd: number
  cwd?: string | null
}

/**
 * operatorsVM is the YOUR AGENTS Operators tier: every Operator across the
 * workstreams (owners of unfinished steps, plus native chats named after the
 * bead), in workstream rank order.
 */
export function operatorsVM(s: Snapshot, chats: Record<string, OperatorChat>): OperatorVM[] {
  const out: OperatorVM[] = []
  const chatRoles = Object.keys(chats)
  for (const ws of s.workstreams) {
    for (const role of operatorRoles(ws, chatRoles)) {
      const chat = chats[role]
      const native = !!chat?.running
      const owned = flatten(ws.steps ?? []).find((st) => st.owner === role && !isDone(st))
      const cwd = chat?.cwd?.replace(/\/+$/, '')
      out.push({
        role,
        wsKey: wsKey(ws),
        wsTitle: ws.title,
        label: operatorLabel(role, ws.bead_id),
        task: owned?.title ?? ws.title,
        branch: cwd ? cwd.slice(cwd.lastIndexOf('/') + 1) : undefined,
        // The claim is the bead's; the process only says how it is going. A
        // claim whose process exited reads finished (it reported) or died.
        state: (!native && owned?.owner_state !== 'working' && owned?.owner_state) || (native ? chat.state : s.agents?.[role]?.state) || (native ? 'idle' : 'tmux'),
        native,
        cost: native ? chat.total_cost_usd : undefined,
      })
    }
  }
  return out
}

/** How long an Operator that ended stays in the Operators tier (cmdrd sends the last 10 min). */
export const FINISHED_SECS = 10 * 60

export interface FinishedOperatorVM {
  role: string
  wsKey: string
  wsTitle: string
  label: string
  died: boolean
  state: string // "finished 2m ago" / "died 2m ago"
  report: string // first line of its final report, or why it died
}

/** finishedOperatorsVM: Operators that ended in the last 10 minutes, newest first, so a finish is seen. */
export function finishedOperatorsVM(s: Snapshot, nowSec: number): FinishedOperatorVM[] {
  const out: (FinishedOperatorVM & { at: number })[] = []
  for (const ws of s.workstreams) {
    for (const f of s.watch?.[ws.manager_role ?? '']?.finished ?? []) {
      if (nowSec - f.at > FINISHED_SECS) continue
      const age = nowSec - f.at
      out.push({
        role: f.role,
        wsKey: wsKey(ws),
        wsTitle: ws.title,
        label: operatorLabel(f.role, ws.bead_id),
        died: !!f.died,
        state: `${f.died ? 'died' : 'finished'} ${age < 60 ? 'just now' : `${fmtAge(age)} ago`}`,
        report: f.report ?? '',
        at: f.at,
      })
    }
  }
  return out.sort((a, b) => b.at - a.at).map(({ at: _at, ...vm }) => vm)
}

// ---- a Manager monitoring its work ------------------------------------------------

/** An Operator is quiet when it has written nothing for this long (cmdrd tells its Manager). */
export const QUIET_SECS = 20 * 60

/** One thing a Manager between turns is watching: an Operator at work, or a wait it recorded. */
export interface WatchItem {
  key: string
  kind: 'operator' | 'ci' | 'release' | 'person' | 'issue'
  title: string // the bead (or, for Commander, the workstream) it is about
  what: string // "Operator, active 20s ago" / "CI on 4 PRs" / "review by reviewer-a"
  since?: number // unix s it began, when known (a wait's record, an Operator's spawn)
  slow: boolean // a quiet Operator; CI running SLOW_CI_SECS or more
  open: { agent: string; ws: string } | { bead: string } | { ws: string }
}

export interface WatchVM {
  items: WatchItem[] // slow ones first, then Operators, CI, people
  summary: string // what is watched: "1 Operator at work · main CI · prod tag · review: reviewer-a, reviewer-b"
  checkedAt?: number // unix s of cmdrd's last look (the watch's, or its poll of the recorded waits)
  checkinAt?: number // unix s of the next check-in
}

const depth = (id: string) => id.split('.').length

/**
 * watchVM is what a Manager between turns is monitoring: its running Operators
 * (from cmdrd's minute watch when there is one, else from the beads they own)
 * and the CI and reviews it recorded waits on before its next gate.
 */
export function watchVM(s: Snapshot, role: string, chats: Record<string, OperatorChat>, nowSec: number): WatchVM | null {
  if (role === 'commander') return commanderWatchVM(s)
  const owner = managerOfRole(s, role)
  if (!owner) return null
  const ws = owner.ws
  // A nested Manager watches its own subtree.
  const all = flatten(owner.step ? (owner.step.children ?? []) : (ws.steps ?? []))
  const beads = new Map(all.map((b) => [b.bead_id, b]))
  const w = s.watch?.[role]
  const items: WatchItem[] = []

  const ops = w
    ? w.operators.map((o) => ({ role: o.role, bead: o.bead_id, active: o.active_at, since: o.since }))
    : operatorsVM({ ...s, workstreams: [ws] }, chats).map((o) => ({ role: o.role, bead: all.find((b) => b.owner === o.role)?.bead_id ?? '', active: undefined, since: undefined }))
  for (const o of ops) {
    const idle = o.active !== undefined ? nowSec - o.active : undefined
    const quiet = idle !== undefined && idle >= QUIET_SECS
    const what = idle === undefined ? 'Operator at work' : quiet ? `Operator quiet ${fmtAge(idle)}` : `Operator, active ${idle < 60 ? 'just now' : `${fmtAge(idle)} ago`}`
    items.push({ key: o.role, kind: 'operator', title: plainTitle(beads.get(o.bead)?.title ?? '') || 'Untitled step', what, since: o.since, slow: quiet, open: { agent: o.role, ws: wsKey(ws) } })
  }

  // Waits before the next gate (all open work when there are no gates), each
  // once, on the deepest bead that records it; "you" is the Needs-you list's.
  const stretch = gateStatus(ws)?.waitBeads ?? all.filter((b) => b.status !== 'closed' && b.state !== 'done' && !b.gate && b.waiting)
  const seen = new Set<string>()
  let waitChecked = 0
  for (const b of [...stretch].sort((x, y) => depth(y.bead_id) - depth(x.bead_id))) {
    const waits = (b.waiting ?? '').split(' · ').filter((x) => x && x !== 'you' && !seen.has(x))
    waits.forEach((x) => seen.add(x))
    const ci = waits.filter((x) => x.startsWith('CI'))
    const release = waits.filter((x) => /^(main CI|prod tag)\b/.test(x))
    const people = waits.filter((x) => !ci.includes(x) && !release.includes(x))
    const title = plainTitle(b.title)
    if (b.waiting_checked) waitChecked = Math.max(waitChecked, b.waiting_checked)
    if (ci.length) {
      const what = ci.length > 1 ? `CI on ${ci.length} PRs` : `CI on ${ci[0].replace(/^CI\s*/, '')}`
      const slow = !!b.waiting_since && nowSec - b.waiting_since >= SLOW_CI_SECS
      items.push({ key: `${b.bead_id}:ci`, kind: 'ci', title, what, since: b.waiting_since, slow, open: { bead: b.bead_id } })
    }
    for (const r of release) {
      items.push({ key: `${b.bead_id}:${r}`, kind: 'release', title, what: r, since: b.waiting_since, slow: false, open: { bead: b.bead_id } })
    }
    for (const p of people) {
      const until = b.waiting_until_as === p ? b.waiting_until : undefined
      const what = until ? `${p} · ${clock(until)}` : p.replace(/^review \((.*)\)$/, 'review by $1')
      items.push({ key: `${b.bead_id}:${p}`, kind: 'person', title, what, since: b.waiting_since, slow: false, open: { bead: b.bead_id } })
    }
  }
  // At a gate others approve, the Manager is watching for their approval.
  const gs = gateStatus(ws)
  const approvers = (gs?.gate.approvers ?? []).filter((a) => a !== 'user')
  if (gs?.at && approvers.length) {
    items.push({ key: `${gs.gate.bead_id}:gate`, kind: 'person', title: plainTitle(gs.gate.title), what: `approval by ${approvers.join(', ')}`, since: gs.gate.waiting_since, slow: false, open: { bead: gs.gate.bead_id } })
  }
  if (!items.length) return null

  const order = { issue: 0, operator: 0, ci: 1, release: 1, person: 2 }
  items.sort((a, b) => Number(b.slow) - Number(a.slow) || order[a.kind] - order[b.kind])
  const summary = watchSummary(items)
  return {
    items,
    summary,
    checkedAt: Math.max(w?.checked_at ?? 0, waitChecked) || undefined,
    checkinAt: w?.checkin_at,
  }
}

/**
 * watchSummary names what is watched, by kind: "1 Operator at work · CI on 2
 * PRs · main CI · prod tag · review: reviewer-a, reviewer-b". Plain wait text (a person's
 * login, an Operator) is the label itself; nothing is counted as "people".
 */
export function watchSummary(items: WatchItem[]): string {
  const nOps = items.filter((i) => i.kind === 'operator').length
  const prs = items.filter((i) => i.kind === 'ci').reduce((n, i) => n + (Number(/on (\d+) PRs/.exec(i.what)?.[1]) || 1), 0)
  const ciNames = items.filter((i) => i.kind === 'ci' && !/ PRs$/.test(i.what)).map((i) => i.what.replace(/^CI on /, ''))
  const releases = [...new Set(items.filter((i) => i.kind === 'release').map((i) => i.what.replace(/\s+[0-9a-f]{7,40}$/, '')))]
  const reviewers = items.filter((i) => i.what.startsWith('review by ')).map((i) => i.what.replace(/^review by /, ''))
  const others = items.filter((i) => i.kind === 'person' && !i.what.startsWith('review by ')).map((i) => i.what)
  return [
    nOps && `${nOps} Operator${nOps > 1 ? 's' : ''} at work`,
    prs && (prs === 1 && ciNames.length === 1 ? `CI ${ciNames[0]}` : `CI on ${prs} PRs`),
    ...releases,
    reviewers.length && `review: ${reviewers.join(', ')}`,
    ...others,
  ]
    .filter(Boolean)
    .join(' · ')
}

/**
 * commanderWatchVM is Commander between turns: every workstream under cmdrd's
 * minute watch, with what has gone wrong. Absent until cmdrd runs the watch.
 */
function commanderWatchVM(s: Snapshot): WatchVM | null {
  const w = s.watch?.commander
  if (!w) return null
  const items: WatchItem[] = (w.issues ?? []).map((i) => {
    const ws = s.workstreams.find((x) => x.bead_id === i.workstream)
    return { key: `${i.workstream}:${i.text}`, kind: 'issue', title: ws?.title ?? i.workstream, what: i.text.replace(/\s*\(.*\)$/, ''), since: i.since, slow: true, open: { ws: ws ? wsKey(ws) : i.workstream } }
  })
  const n = s.workstreams.length
  return {
    items,
    summary: `${n} workstream${n === 1 ? '' : 's'}${items.length ? `, ${items.length} need${items.length === 1 ? 's' : ''} attention` : ''}`,
    checkedAt: w.checked_at,
    checkinAt: w.checkin_at,
  }
}

/** isGateBead reports whether the workstream has a human approval gate with this bead id. */
export function isGateBead(ws: SnapWorkstream, beadId: string): boolean {
  const find = (steps: SnapStep[]): boolean => steps.some((s) => (s.bead_id === beadId && !!s.gate) || find(s.children ?? []))
  return find(ws.steps ?? [])
}

/**
 * roleLabel names an agent by where it works: "Commander", "5 Audits", or
 * "5 Audits › Log audits" for one in a track. A role matches the
 * workstream and track whose bead id it carries (dots or dashes); anything
 * else, such as an unmapped mail name, is shown as is.
 */
export function roleLabel(s: Snapshot | null | undefined, role: string): string {
  if (role === 'commander') return 'Commander'
  const flat = role.replace(/\./g, '-')
  const carries = (id: string) => {
    const d = id.replace(/\./g, '-')
    const i = flat.indexOf(d)
    return i > 0 && flat[i - 1] === '-' && (i + d.length === flat.length || flat[i + d.length] === '-')
  }
  let best: SnapWorkstream | undefined
  for (const ws of s?.workstreams ?? []) {
    if (ws.manager_role === role || carries(ws.bead_id)) {
      if (!best || ws.manager_role === role || ws.bead_id.length > best.bead_id.length) best = ws
      if (ws.manager_role === role) break
    }
  }
  if (!best) return role
  const head = `${best.n} ${splitTitle(best.title)}`
  if (best.manager_role === role) return head
  const track = openNested(best).filter((t) => carries(t.bead_id)).sort((a, b) => b.bead_id.length - a.bead_id.length)[0]
  return track ? `${head} › ${splitTitle(track.title)}` : head
}

const splitTitle = (t: string) => splitTickets(t).text
