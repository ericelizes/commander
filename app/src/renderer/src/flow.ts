// The workstream flow: what the right column's graph draws. Each track (or the
// workstream, when it has one track or none) is a lane; each open stage is a
// box in plan order with who has it and what it waits on and its open
// sub-steps, followed by the gates it must pass. Done stages collapse to a
// count at the top of the lane.

import type { SnapGate, SnapStep, SnapWorkstream } from '../../shared/snapshot'
import { isDone, isTrack, openLeaves, parked } from './viewmodel'

export type BriefTone = 'done' | 'working' | 'waiting' | 'needs' | 'missing' | 'idle'

export interface BriefRow {
  key: string
  kind: 'step' | 'gate' | 'more'
  depth: 0 | 1
  title: string
  tone: BriefTone
  detail: string // who has it, or what it waits on, in words
}

export interface FlowNode {
  id: string
  step: SnapStep // the bead, for opening its children
  row: BriefRow // the stage itself
  subs: BriefRow[] // its open sub-steps, then "n more"
  gates: BriefRow[] // the gates it must pass, in order
  dep: boolean // the box above is one it really waits on (else it is just next in the plan)
}

export interface FlowLane {
  key: string
  title: string // the track's title; "" for a workstream without tracks
  beadId: string
  done: number // its finished stages, drawn as one box
  nodes: FlowNode[]
}

const MAX_SUBSTEPS = 4

// isYou: the app's user as a gate's verifier or approver ("user"; "user" in beads written before the rename).
const isYou = (who: string) => who === 'user' || who === 'user'

const clip = (t: string) => (t.length > 60 ? `${t.slice(0, 59)}…` : t)

/** gateWho is who passes a gate, in words. */
export function gateWho(verifier: string): string {
  if (verifier === 'auto') return 'auto check'
  if (verifier === 'operator') return 'Operator check'
  if (verifier === 'user') return 'you'
  if (verifier.startsWith('reviewer:')) return `review by ${verifier.slice('reviewer:'.length)}`
  return verifier
}

function gateRow(g: SnapGate, key: string, reached: boolean): BriefRow {
  const who = gateWho(g.verifier)
  if (g.status === 'passed') return { key, kind: 'gate', depth: 1, title: g.name, tone: 'done', detail: `Passed · ${who}` }
  if (g.status === 'failed') return { key, kind: 'gate', depth: 1, title: g.name, tone: 'needs', detail: `Failed · ${who}` }
  if (g.verifier === 'user') return { key, kind: 'gate', depth: 1, title: g.name, tone: reached ? 'needs' : 'idle', detail: reached ? 'Needs your OK' : 'Your OK, once the work is done' }
  return { key, kind: 'gate', depth: 1, title: g.name, tone: reached ? 'waiting' : 'idle', detail: reached ? `Waiting for ${who}` : `Then ${who}` }
}

function ownerName(owner: string | undefined): string {
  if (!owner) return ''
  if (owner.startsWith('operator-')) return 'Operator'
  if (owner.startsWith('manager-')) return 'Manager'
  if (owner === 'user') return 'you'
  return owner
}

// waitWords says an agent wait in words: "operator-op-run-10-app-..." is an Operator.
const waitWords = (w: string) => w.split(' · ').map((x) => (x.startsWith('operator-') ? 'an Operator' : x.startsWith('manager-') ? 'a Manager' : x)).join(' · ')

/** approverWords is who approves a gate bead, in words. */
export function approverWords(s: SnapStep): string {
  const who = (s.approvers ?? []).map((a) => (a === 'user' ? 'you' : a))
  return who.length ? who.join(', ') : 'approval'
}

/** stepRow is one step: its tone and the words for who has it or what it waits on. */
export function stepRow(s: SnapStep, depth: 0 | 1, titleOf: (id: string) => string | undefined): BriefRow {
  const base = { key: s.bead_id, kind: 'step' as const, depth, title: s.title }
  if (isDone(s)) return { ...base, tone: 'done', detail: 'Done' }
  // Working means an agent is on this step: its Operator's process is alive, or
  // it is the one step the Manager is on. A Manager in a turn marks every step
  // it owns as working, so that flag alone does not say it.
  const blockers = (s.blocked_by ?? []).map(titleOf).filter((t): t is string => !!t)
  const onIt = s.owner_state === 'working' || (!!s.working && (s.owner?.startsWith('operator-') ? true : !!s.now && !blockers.length))
  if (onIt) return { ...base, tone: 'working', detail: `${ownerName(s.owner) || 'Agent'} working` }
  // A step behind open work is next, not now, whatever ask or wait it already carries.
  if (blockers.length) return { ...base, tone: 'idle', detail: `After ${clip(blockers[0])}${blockers.length > 1 ? ` +${blockers.length - 1}` : ''}` }
  if (s.state === 'needs-user' || s.ask || (s.gate && s.approvers?.includes('user'))) return { ...base, tone: 'needs', detail: s.ask ? `Needs you: ${s.ask}` : 'Needs you' }
  if (s.waiting) return { ...base, tone: s.waiting === 'you' ? 'needs' : 'waiting', detail: s.waiting === 'you' ? 'Needs you' : `Waiting for ${waitWords(s.waiting)}` }
  if (s.broken) return { ...base, tone: 'missing', detail: 'Nobody on it' }
  if (s.owner_state === 'died') return { ...base, tone: 'missing', detail: 'Its Operator stopped' }
  if (s.owner) return { ...base, tone: 'idle', detail: `${ownerName(s.owner)} has it` }
  return { ...base, tone: 'idle', detail: 'Not started' }
}

function lane(key: string, title: string, beadId: string, steps: SnapStep[], titleOf: (id: string) => string | undefined): FlowLane {
  const live = planOrder(steps.filter((s) => !parked(s) && !s.superseded && !isTrack(s)))
  const nodes: FlowNode[] = []
  let done = 0
  let prev = ''
  for (const s of live) {
    if (isDone(s)) {
      done++
      continue
    }
    const open = (s.children ?? []).filter((c) => !parked(c) && !c.superseded && !isDone(c) && !c.gate)
    const subs = open.slice(0, MAX_SUBSTEPS).map((c) => stepRow(c, 1, titleOf))
    if (open.length > MAX_SUBSTEPS) subs.push({ key: `${s.bead_id}:more`, kind: 'more', depth: 1, title: `${open.length - MAX_SUBSTEPS} more steps`, tone: 'idle', detail: '' })
    // A gate is reached once its stage's work is done: every sub-step, or for a
    // stage without sub-steps, the stage itself waiting on review or on you.
    const kids = (s.children ?? []).filter((c) => !c.gate && !parked(c) && !c.superseded)
    const blocked = (s.blocked_by ?? []).some((id) => !!titleOf(id))
    const reached = !blocked && (kids.length ? kids.every(isDone) : s.state === 'review' || s.state === 'needs-user')
    const gates = (s.gates ?? []).filter((g) => g.status !== 'passed').map((g) => gateRow(g, `${s.bead_id}:gate:${g.bead_id ?? g.name}`, reached))
    nodes.push({ id: s.bead_id, step: s, row: stepRow(s, 0, titleOf), subs, gates, dep: !!prev && (s.blocked_by ?? []).includes(prev) })
    prev = s.bead_id
  }
  return { key, title, beadId, done, nodes }
}

/** planOrder puts each step after the siblings it waits on, else keeps their order. */
export function planOrder(steps: SnapStep[]): SnapStep[] {
  const ids = new Set(steps.map((s) => s.bead_id))
  const out: SnapStep[] = []
  const placed = new Set<string>()
  const visiting = new Set<string>()
  const byId = new Map(steps.map((s) => [s.bead_id, s]))
  const place = (s: SnapStep) => {
    if (placed.has(s.bead_id) || visiting.has(s.bead_id)) return
    visiting.add(s.bead_id)
    for (const b of s.blocked_by ?? []) if (ids.has(b)) place(byId.get(b)!)
    visiting.delete(s.bead_id)
    placed.add(s.bead_id)
    out.push(s)
  }
  steps.forEach(place)
  return out
}

/** flowVM is a workstream as lanes: one per open track when it has two or more, else one. */
export function flowVM(ws: SnapWorkstream): FlowLane[] {
  const byId = new Map<string, SnapStep>()
  const index = (steps: SnapStep[]) => steps.forEach((s) => (byId.set(s.bead_id, s), index(s.children ?? [])))
  index(ws.steps ?? [])
  const titleOf = (id: string) => {
    const s = byId.get(id)
    return s && !isDone(s) ? s.title : undefined
  }
  const tracks = openLeaves(ws)
  if (tracks.length > 1) {
    const own = lane(`${ws.bead_id}:own`, 'Outside any workstream', ws.bead_id, ws.steps ?? [], titleOf)
    const out = tracks.map((t) => lane(t.bead_id, t.title, t.bead_id, t.children ?? [], titleOf))
    return own.nodes.length ? [...out, own] : out
  }
  const steps = tracks.length === 1 ? [...(ws.steps ?? []).filter((s) => !isTrack(s)), ...(tracks[0].children ?? [])] : ws.steps ?? []
  return [lane(ws.bead_id, '', ws.bead_id, steps, titleOf)]
}

/** isNow: a node something is happening on (an agent, a wait, you), not just next in line. */
export const isNow = (n: FlowNode) => n.row.tone !== 'idle' && n.row.tone !== 'done'

/** openChildren are a bead's open sub-steps (not gates) as rows, each with its own children. */
export function openChildren(s: SnapStep, titleOf: (id: string) => string | undefined = () => undefined): { row: BriefRow; step: SnapStep }[] {
  return (s.children ?? []).filter((c) => !parked(c) && !c.superseded && !isDone(c) && !c.gate).map((c) => ({ row: stepRow(c, 1, titleOf), step: c }))
}

export type GateStatus = 'done' | 'now' | 'pending' | 'later'

export interface GateItem {
  id: string
  title: string
  who: string // "you", "reviewer-a"
  yours: boolean
  status: GateStatus // done; up now; next but the work before it is open; later
}

/** A lane read top to bottom: the work up to a gate, the gate, the work up to the next, and so on. */
export type Segment = { kind: 'work'; key: string; nodes: FlowNode[]; done: number } | { kind: 'gate'; key: string; gate: GateItem }

const isPersonGate = (verifier: string) => verifier === 'user' || verifier.startsWith('reviewer:')

/**
 * segments splits a lane's steps at the gates a person approves (gate beads,
 * and the you/reviewer gates on stages; automatic and Operator checks are not
 * gates here). The first open gate is up now when the work before it is done,
 * else pending; the ones after it come later.
 */
export function segments(steps: SnapStep[], titleOf: (id: string) => string | undefined): Segment[] {
  const out: Segment[] = []
  const reached: boolean[] = []
  const done: boolean[] = []
  let work: Extract<Segment, { kind: 'work' }> | null = null
  let prev = '' // the bead just above in the lane, and the gate that opens this stretch:
  let opener = '' // "after" either goes without saying
  const addGate = (g: Omit<GateItem, 'status'>, isDoneGate: boolean, isReached: boolean) => {
    out.push({ kind: 'gate', key: `gate:${g.id}`, gate: { ...g, status: 'later' } })
    done.push(isDoneGate)
    reached.push(isReached)
    work = null
    prev = g.id
    opener = g.id
  }
  for (const s of planOrder(steps.filter((x) => !parked(x) && !x.superseded && !isTrack(x)))) {
    if (s.gate) {
      const who = approverWords(s)
      addGate({ id: s.bead_id, title: s.title, who, yours: who.split(', ').includes('you') }, isDone(s), !(s.blocked_by ?? []).some((id) => !!titleOf(id)))
      continue
    }
    if (!work) {
      work = { kind: 'work', key: `work:${s.bead_id}`, nodes: [], done: 0 }
      out.push(work)
    }
    if (isDone(s)) work.done++
    else {
      const row = stepRow(s, 0, titleOf)
      const open = (s.blocked_by ?? []).filter((id) => !!titleOf(id))
      if (row.detail.startsWith('After ') && open.length === 1 && (open[0] === prev || open[0] === opener)) row.detail = ''
      work.nodes.push({ id: s.bead_id, step: s, row, subs: [], gates: [], dep: false })
    }
    prev = s.bead_id
    const kids = (s.children ?? []).filter((c) => !c.gate && !parked(c) && !c.superseded)
    for (const g of s.gates ?? []) {
      if (!isPersonGate(g.verifier)) continue
      const isReached = kids.length ? kids.every(isDone) : isDone(s) || s.state === 'review' || s.state === 'needs-user'
      addGate({ id: g.bead_id ?? `${s.bead_id}:${g.name}`, title: g.name, who: gateWho(g.verifier).replace(/^review by /, ''), yours: g.verifier === 'user' }, g.status === 'passed', isReached)
    }
  }
  let first = true
  let gi = 0
  for (const seg of out) {
    if (seg.kind !== 'gate') continue
    const i = gi++
    if (done[i]) seg.gate.status = 'done'
    else if (first) {
      seg.gate.status = reached[i] ? 'now' : 'pending'
      first = false
    }
  }
  return out
}

/** laneSegments is segments for each of flowVM's lanes, by lane key. */
export function laneSegments(ws: SnapWorkstream): Record<string, Segment[]> {
  const byId = new Map<string, SnapStep>()
  const index = (steps: SnapStep[]) => steps.forEach((s) => (byId.set(s.bead_id, s), index(s.children ?? [])))
  index(ws.steps ?? [])
  const titleOf = (id: string) => {
    const s = byId.get(id)
    return s && !isDone(s) ? s.title : undefined
  }
  const out: Record<string, Segment[]> = {}
  const tracks = openLeaves(ws)
  if (tracks.length > 1) {
    for (const t of tracks) out[t.bead_id] = segments(t.children ?? [], titleOf)
    out[`${ws.bead_id}:own`] = segments(ws.steps ?? [], titleOf)
  } else {
    const steps = tracks.length === 1 ? [...(ws.steps ?? []).filter((s) => !isTrack(s)), ...(tracks[0].children ?? [])] : ws.steps ?? []
    out[ws.bead_id] = segments(steps, titleOf)
  }
  return out
}

/**
 * LaneParts orders a lane by what matters now: past (approved gates and done
 * steps, folded to a count), current (the open work up to the first open gate,
 * and that gate), next (the stretch after it up to the following gate), later
 * (the rest, folded to a count of gates).
 */
export interface LaneParts {
  past: Segment[]
  approved: number
  doneSteps: number
  current: Segment[]
  next: Segment[]
  later: Segment[]
  laterGates: number
}

export function laneParts(segs: Segment[]): LaneParts {
  const open = segs.findIndex((s) => s.kind === 'gate' && s.gate.status !== 'done')
  const end = open < 0 ? segs.length : open
  let lastDone = -1
  segs.forEach((s, i) => s.kind === 'gate' && s.gate.status === 'done' && i < end && (lastDone = i))
  const past = segs.slice(0, lastDone + 1)
  const current = segs.slice(lastDone + 1, open < 0 ? segs.length : open + 1)
  const after = open < 0 ? [] : segs.slice(open + 1)
  const nextGate = after.findIndex((s) => s.kind === 'gate')
  const next = nextGate < 0 ? after : after.slice(0, nextGate + 1)
  const later = nextGate < 0 ? [] : after.slice(nextGate + 1)
  // Steps still open from before the last approved gate belong with the current work.
  const stragglers = past.flatMap((s) => (s.kind === 'work' ? s.nodes : []))
  const doneSteps = [...past, ...current].reduce((n, s) => n + (s.kind === 'work' ? s.done : 0), 0)
  const strip = (s: Segment): Segment => (s.kind === 'work' ? { ...s, done: 0 } : s)
  const cur = current.map(strip).filter((s) => s.kind === 'gate' || s.nodes.length > 0)
  if (stragglers.length) cur.unshift({ kind: 'work', key: 'work:earlier', nodes: stragglers, done: 0 })
  return {
    past: past.map((s) => (s.kind === 'work' ? { ...s, nodes: [] } : s)),
    approved: past.filter((s) => s.kind === 'gate').length,
    doneSteps,
    current: cur,
    next: next.map(strip),
    later: later.map(strip),
    laterGates: later.filter((s) => s.kind === 'gate').length,
  }
}

/**
 * StageRow is one row of a lane read as stages, the way gates were agreed:
 * the gate's stage name ("PR approved"), the stage's condition under it, who
 * has it or who checks it, and where it stands.
 */
export interface StageRow {
  id: string
  step: SnapStep
  label: string // the gate's stage name, else the stage title
  sub?: string // the stage title (its condition) when the label is the gate's name
  status: 'done' | 'needs' | 'working' | 'waiting' | 'missing' | 'next' | 'later'
  right: string // who has it now, or who checks it
}

const GATE_NAMES: Record<string, string> = {
  ready: 'Ready', 'pr-ready': 'PR ready', 'pr-approved': 'PR approved', review: 'Review', merged: 'Merged', deployed: 'Deployed',
  live: 'Live', pilot: 'Pilot', 'vm-setup': 'VM setup', 'works-e2e': 'Works end to end', investigation: 'Investigation',
  outward: 'Outward message', spend: 'Spend', pushed: 'Pushed', 'ci-green': 'CI green', spike: 'Spike', workflow: 'Workflow',
}

/** gateName is a gate's stage name in words: "pr-approved" is "PR approved". */
export function gateName(name: string): string {
  if (GATE_NAMES[name]) return GATE_NAMES[name]
  const w = name.replace(/[-_]+/g, ' ').trim()
  return w ? w[0].toUpperCase() + w.slice(1) : name
}

// checker is who checks a gate, in words.
function checker(verifier: string): string {
  if (verifier === 'auto') return 'automatic'
  if (isYou(verifier)) return 'you'
  if (verifier === 'operator') return 'Operator checks'
  if (verifier.startsWith('reviewer:')) return verifier.slice('reviewer:'.length)
  return verifier
}

/** stageRows reads a lane's top-level steps as stages, in plan order. */
export function stageRows(steps: SnapStep[], titleOf: (id: string) => string | undefined): StageRow[] {
  return planOrder(steps.filter((s) => !parked(s) && !s.superseded && !isTrack(s))).map((s): StageRow => {
    const blocked = (s.blocked_by ?? []).some((id) => !!titleOf(id))
    if (s.gate) {
      const who = approverWords(s)
      if (isDone(s)) return { id: s.bead_id, step: s, label: s.title, status: 'done', right: '' }
      if (!blocked) return { id: s.bead_id, step: s, label: s.title, status: who === 'you' ? 'needs' : 'waiting', right: who === 'you' ? 'needs you' : `waiting on ${who}` }
      return { id: s.bead_id, step: s, label: s.title, status: 'later', right: who }
    }
    const g = s.gates?.[0]
    const base = { id: s.bead_id, step: s, label: g ? gateName(g.name) : s.title, sub: g ? s.title : undefined }
    if (isDone(s)) return { ...base, status: 'done', right: '' }
    const row = stepRow(s, 0, titleOf)
    if (row.tone === 'working') return { ...base, status: 'working', right: row.detail }
    if (row.tone === 'needs') return { ...base, status: 'needs', right: 'needs you' }
    if (row.tone === 'waiting') return { ...base, status: 'waiting', right: row.detail.replace(/^Waiting for /, 'waiting for ') }
    if (row.tone === 'missing') return { ...base, status: 'missing', right: row.detail.toLowerCase() }
    const who = g ? checker(g.verifier) : ''
    return { ...base, status: blocked ? 'later' : 'next', right: who }
  })
}

/** laneStages is stageRows for each of flowVM's lanes, by lane key. */
export function laneStages(ws: SnapWorkstream): Record<string, StageRow[]> {
  const byId = new Map<string, SnapStep>()
  const index = (steps: SnapStep[]) => steps.forEach((s) => (byId.set(s.bead_id, s), index(s.children ?? [])))
  index(ws.steps ?? [])
  const titleOf = (id: string) => {
    const s = byId.get(id)
    return s && !isDone(s) ? s.title : undefined
  }
  const out: Record<string, StageRow[]> = {}
  const tracks = openLeaves(ws)
  if (tracks.length > 1) {
    for (const t of tracks) out[t.bead_id] = stageRows(t.children ?? [], titleOf)
    out[`${ws.bead_id}:own`] = stageRows(ws.steps ?? [], titleOf)
  } else {
    const steps = tracks.length === 1 ? [...(ws.steps ?? []).filter((s) => !isTrack(s)), ...(tracks[0].children ?? [])] : ws.steps ?? []
    out[ws.bead_id] = stageRows(steps, titleOf)
  }
  return out
}
