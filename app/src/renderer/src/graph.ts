// The task canvas is the beads, raw. Every bead is one shape:
//   a bead with children is a box holding them (a workstream is such a bead)
//   a bead without children is a card
//   an arrow is a real beads "blocks" dependency (blocked_by), never guessed from order
// Waited-on beads stack first. Who works on what is a tag on the card or box.

import { checkTone, stageGateTones, stageReached, stepGateTone, type GateTone } from './gateTone'
import type { GateStatus, GraphLevel, SnapGate, SnapGateNode, SnapStep, SnapWorkstream, Snapshot, StepState } from '../../shared/snapshot'
import { wsKey } from './viewmodel'
import { splitTickets } from '../../features/linear/Ticket'

export type GateKind = 'yours' | 'reviewer' | 'operator' | 'auto'

// GateInput is a bead a gate band lists as feeding it: a child of the gate or
// a bead it waits on. A finished one is muted.
export interface GateInput {
  id: string // bead id
  title: string
  state: StepState
  status: string // the bead's own status
  muted: boolean
}

// SpineTask is a task shown on a stage row of the spine.
export interface SpineTask {
  id: string
  title: string
  state: StepState
  status: string
}

// Marker is a "blocked by X" line on a bead: a real blocks dependency that is
// not drawn as an arrow (the blocker is not the bead right above it, or it is
// hidden at this level).
export interface Marker {
  id: string // the blocking bead
  title: string
  closed: boolean
  hidden?: boolean // not on the canvas at this level
}

// SpineInfo is what a row of the Gates and Stages levels says besides its own
// line: the beads hidden at this level that hang off it (open ones inline, a
// few), a count for the rest, and for a stage gate the stage it guards.
export interface SpineInfo {
  tasks: SpineTask[] // inline: open ones (max SPINE_TASKS), plus the done ones once expanded
  done: number // finished beads attached to the row, listed or not
  more: number // open beads attached and not shown
  expanded: boolean
  guards?: string // a stage gate: the stage it guards
}

interface Bead {
  id: string
  title: string
  state: StepState
  status?: string // the bead's own beads status (open, in_progress, closed ...)
  parkedUntil?: number // unix s a parked (deferred) bead comes back
  x: number
  y: number
  w: number
  h: number
  now?: boolean // an agent is working on it right now
  review?: string // its PR
  until?: string // owned: its owner keeps at it until this finish line
  change?: 'add' | 'remove' | 'edit' // a proposed change, in a graph an agent drew
  waiting?: string // what it waits on: "review (reviewer-a, reviewer-b)", "CI #104"
  gate?: boolean // a human approval gate
  track?: boolean // a track of its workstream (a group of its own gates and work)
  approvers?: string[]
  gates?: SnapGate[] // checks it must pass before it closes, drawn as a checklist
  gateTones?: GateTone[] // the colour of each of its gates
  tone?: GateTone // a gate bead's colour
  gateKind?: GateKind // who a gate bead waits on, for its band's label
  inputs?: GateInput[] // a gate band's inputs: its children and the beads it waits on
  spine?: SpineInfo // a row of the Gates or Stages level
  markers?: Marker[] // blockers not drawn as an arrow
  gateNode?: GateStatus // a stage gate bead (not a step): its own status
  detail?: string // the gate's full write-up, for hover
  agent?: string // who works on it
  gone?: boolean // its claimant's process exited (finished or died): claimed, not working
  wsKey?: string
  beadId?: string
  project?: string
}

export interface Card extends Bead {
  ask?: boolean // opens a decision
}

export interface Box extends Bead {
  kind: 'workstream' | 'bead'
  crew: string[] // everyone on it or on anything inside it, its own agent first
  level?: GraphLevel // a workstream: the level it is drawn at
  hidden?: number // a workstream below All: beads hidden at this level that no row carries
}

export interface Edge {
  from: string // card or box id
  to: string
}

export interface Canvas {
  cards: Card[]
  boxes: Box[]
  edges: Edge[]
  width: number
  height: number
}

export const CARD_W = 290
export const CARD_H = 64
export const BOX_HEAD = 58 // a box's title row and its state line
export const GATE_ROW = 18 // one line of a gates checklist

// A gate bead is a band as wide as a workstream box's inside; its text wraps in full.
export const BAND_W = 460 // WS_MIN_W: the inside of a workstream box
const BAND_HEAD = 22 // the strip naming who the gate is for
const BAND_LINE = 16
export const INPUT_ROW = 16 // one input line in a gate band
const BAND_CHARS = 54 // title characters per line at that width
const GATE_CHARS = 30 // a checklist gate name's characters per line on a card
export const GATE_CHARS_BOX = 58 // ... and in a box head

// gateBandH is the room a gate bead's band takes: its strip and the whole title, wrapped.
export function gateBandH(title: string, inputs = 0): number {
  return BAND_HEAD + Math.max(1, Math.ceil(splitTickets(title).text.length / BAND_CHARS)) * BAND_LINE + 20 + inputs * INPUT_ROW + (inputs ? 6 : 0)
}

// gatesH is the room a bead's gates checklist takes under its state line; a
// long gate name wraps onto more lines.
export function gatesH(b: { gates?: SnapGate[] }, perLine = GATE_CHARS): number {
  return (b.gates ?? []).reduce((a, g) => a + Math.max(1, Math.ceil(g.name.length / perLine)) * GATE_ROW, 0)
}
const PAD = 14 // inside a box
const WS_MIN_W = 460
const GAP_SEQ = 30 // between stacked steps (room for an arrow)
const GAP_PAR = 12 // between parallel sub-steps
const GAP_GRID = 40 // between workstream boxes in the fleet view
const MARGIN = 28

type Item =
  | { kind: 'card'; card: Omit<Card, 'x' | 'y' | 'w' | 'h'>; waits?: string[]; orderWaits?: string[] }
  | { kind: 'box'; box: Omit<Box, 'x' | 'y' | 'w' | 'h'>; children: Item[]; sequence: boolean; cols: number; waits?: string[]; orderWaits?: string[] }

// waitingIn is what a bead waits on: its own wait, else the first wait among
// its unfinished descendants (a box blocked on a review says which).
function waitingIn(s: SnapStep): string | undefined {
  if (s.status === 'closed') return undefined
  return s.waiting ?? s.children?.map(waitingIn).find(Boolean)
}

// crewOf is every agent on unfinished work among these items or inside
// them, each once (a finished bead keeps its owner label, not its agent).
function crewOf(items: Item[]): string[] {
  const out = items.flatMap((i) => (i.kind === 'card' ? (i.card.agent && i.card.state !== 'done' ? [i.card.agent] : []) : i.box.crew))
  return [...new Set(out)]
}

const descendants = (s: SnapStep): SnapStep[] => (s.children ?? []).flatMap((c) => [c, ...descendants(c)])

// indexSteps is every step of the workstream by bead id, at any depth.
function indexSteps(ws: SnapWorkstream): Map<string, SnapStep> {
  const all = new Map<string, SnapStep>()
  const index = (list: SnapStep[]) => list.forEach((s) => (all.set(s.bead_id, s), index(s.children ?? [])))
  index(ws.steps)
  return all
}

const idOf = (i: Item) => (i.kind === 'card' ? i.card.id : i.box.id)

/**
 * arrange keeps an arrow only to the bead right above (the only place a
 * straight arrow cannot cross anything); every other blocker becomes a
 * "blocked by" marker on the bead. A blocker outside the workstream is
 * dropped. A gate band lists what it waits on, so it needs no marker.
 */
function arrange(items: Item[], stacked: boolean, marker: (key: string) => Marker | undefined): Item[] {
  items.forEach((item, i) => {
    const prev = i > 0 ? idOf(items[i - 1]) : undefined
    const bead = item.kind === 'card' ? item.card : item.box
    const keep: string[] = []
    const markers: Marker[] = []
    for (const w of item.waits ?? []) {
      if (stacked && w === prev) keep.push(w)
      else if (!bead.inputs) {
        const m = marker(w)
        if (m) markers.push(m)
      }
    }
    item.waits = keep
    if (markers.length) bead.markers = markers
  })
  return items
}

// markerOf describes a bead by its canvas key for a marker line.
function markerOf(ws: SnapWorkstream, all: Map<string, SnapStep>, shown?: Set<string>): (key: string) => Marker | undefined {
  const prefix = `${wsKey(ws)}:`
  return (key) => {
    const s = all.get(key.slice(prefix.length))
    return s ? { id: s.bead_id, title: s.title, closed: s.status === 'closed', hidden: shown && !shown.has(s.bead_id) ? true : undefined } : undefined
  }
}

// gateKindOf is who a gate bead waits on: the user, a reviewer, the Operator or an auto check.
function gateKindOf(ws: SnapWorkstream, s: SnapStep): GateKind {
  if ((s.approvers ?? []).includes('user')) return 'yours'
  const verifier = (ws.gate_nodes ?? []).find((n) => n.bead_id === s.bead_id)?.verifier ?? ''
  if (verifier === 'auto') return 'auto'
  if (verifier === 'operator') return 'operator'
  return verifier.startsWith('reviewer') || s.approvers?.length ? 'reviewer' : 'operator'
}

// gateInputs is what feeds a gate: the beads inside it and the beads it waits on.
function gateInputs(s: SnapStep, all: Map<string, SnapStep>): GateInput[] {
  const seen = new Set<string>()
  const out: GateInput[] = []
  for (const i of [...descendants(s), ...(s.blocked_by ?? []).map((b) => all.get(b))]) {
    if (!i || i.bead_id === s.bead_id || seen.has(i.bead_id)) continue
    seen.add(i.bead_id)
    out.push({ id: i.bead_id, title: i.title, state: i.state, status: i.status, muted: i.status === 'closed' || i.status === 'deferred' })
  }
  return out
}

// beadOf is the card or box fields every step shares, with the level it is drawn at.
function beadOf(ws: SnapWorkstream, s: SnapStep, manager: string, full: boolean) {
  const agent = s.owner && s.owner !== manager ? s.owner : undefined
  return {
    id: `${wsKey(ws)}:${s.bead_id}`,
    title: s.title,
    state: s.state,
    status: s.status,
    parkedUntil: s.parked_until,
    now: s.now,
    review: s.review,
    gate: s.gate,
    track: s.track,
    approvers: s.approvers,
    tone: s.gate ? stepGateTone(s) : undefined,
    gateKind: s.gate ? gateKindOf(ws, s) : undefined,
    detail: s.gate ? (s.decision?.context ?? s.note) : undefined,
    wsKey: wsKey(ws),
    beadId: s.bead_id,
    project: ws.project,
    ...(full
      ? {
          until: s.until,
          change: s.change,
          waiting: waitingIn(s),
          gates: s.gates?.length ? s.gates : undefined,
          gateTones: s.gates?.length ? stageGateTones(ws, s.bead_id, s.gates) : undefined,
          agent: s.until ? s.owner : agent,
          gone: s.owner_state === 'finished' || s.owner_state === 'died',
        }
      : {}),
  }
}

// stepItems is every bead as it is: nothing folded, merged or inferred.
function stepItems(ws: SnapWorkstream, steps: SnapStep[], manager: string, all: Map<string, SnapStep>): Item[] {
  const marker = markerOf(ws, all)
  const waitsById = containerWaits(ws, steps)
  return steps.filter((s) => !s.superseded).map((s): Item => {
    const bead = beadOf(ws, s, manager, true)
    const waits = (s.blocked_by ?? []).map((b) => `${wsKey(ws)}:${b}`)
    const orderWaits = (waitsById.get(s.bead_id) ?? []).map((b) => `${wsKey(ws)}:${b}`)
    const ask = !!s.decision || s.state === 'needs-user'
    // A gate is always a band; what feeds it is listed inside it, not nested.
    if (s.gate) return { kind: 'card', card: { ...bead, inputs: gateInputs(s, all), ask }, waits, orderWaits }
    if (!s.children?.length) return { kind: 'card', card: { ...bead, ask }, waits, orderWaits }
    // Children come after the beads they wait on, so nothing is drawn above its blocker.
    const children = dependencyOrder(stepItems(ws, s.children, manager, all))
    const cols = children.length > 4 ? 2 : 1
    arrange(children, !!s.until || cols === 1, marker)
    const crew = [...new Set([...(bead.agent ? [bead.agent] : []), ...crewOf(children)])]
    return { kind: 'box', box: { ...bead, kind: 'bead', crew }, children, sequence: !!s.until, cols, waits, orderWaits }
  })
}

// dependencyOrder keeps the plan order except that a step comes after the
// steps it waits on, so arrows point down the box.
function dependencyOrder(items: Item[]): Item[] {
  const byId = new Map(items.map((i) => [idOf(i), i]))
  const out: Item[] = []
  const seen = new Set<Item>()
  let cyclic = false
  const visit = (i: Item, path: Set<Item>) => {
    if (path.has(i)) { cyclic = true; return }
    if (seen.has(i)) return
    path.add(i)
    for (const w of i.orderWaits ?? i.waits ?? []) {
      const dep = byId.get(w)
      if (dep) visit(dep, path)
    }
    path.delete(i)
    seen.add(i)
    out.push(i)
  }
  for (const i of items) visit(i, new Set())
  return cyclic ? items : out
}

// Lift descendant dependencies to the containers drawn together. A nested
// visible row owns its own subtree; superseded subtrees carry no weight.
function containerWaits(ws: SnapWorkstream, siblings: SnapStep[]): Map<string, string[]> {
  const roots = new Set(siblings.map((s) => s.bead_id))
  const owner = new Map<string, string>()
  const walk = (steps: SnapStep[], container?: string) => {
    for (const s of steps) {
      if (s.superseded) continue
      const home = roots.has(s.bead_id) ? s.bead_id : container
      if (home) owner.set(s.bead_id, home)
      walk(s.children ?? [], home)
    }
  }
  walk(ws.steps)
  const waits = new Map(siblings.map((s) => [s.bead_id, new Set<string>()]))
  const all = indexSteps(ws)
  const inside = new Map(siblings.map((s) => [s.bead_id, new Set([s.bead_id, ...descendants(s).map((d) => d.bead_id)])]))
  for (const [id, home] of owner) {
    for (const b of all.get(id)?.blocked_by ?? []) {
      if (inside.get(home)!.has(b)) continue // a container never waits on its own contents
      const blocker = owner.get(b)
      if (blocker !== home) waits.get(home)!.add(blocker ?? b)
    }
  }
  return dropCycleEdges(new Map([...waits].map(([id, deps]) => [id, [...deps]])))
}

/**
 * dropCycleEdges removes the waits that lie on a cycle (both ends in one
 * strongly connected group): a container whose child feeds a sibling that in
 * turn blocks another child has no vertical order that honours both, so that
 * group keeps plan order while every wait outside it still orders the rest.
 */
export function dropCycleEdges(waits: Map<string, string[]>): Map<string, string[]> {
  return new Map([...waits].map(([id, deps]) => [id, deps.filter((d) => !waitsOn(waits, d, id))]))
}

/** waitsOn: `from` is `target`, or waits on it through any chain. */
export function waitsOn(waits: Map<string, string[]>, from: string, target: string): boolean {
  const seen = new Set<string>()
  const stack = [from]
  while (stack.length) {
    const id = stack.pop()!
    if (id === target) return true
    if (seen.has(id)) continue
    seen.add(id)
    stack.push(...(waits.get(id) ?? []))
  }
  return false
}

/**
 * workDepth is how far down the work each step sits: 0 with nothing to wait
 * on, else one below its deepest blocker (blocks edges at any depth; a
 * superseded blocker no longer holds anything up). Listing steps by it, plan
 * order within a depth, puts every prerequisite above what it blocks, also
 * when the blocking is through a bead the level does not draw.
 */
export function workDepth(ws: SnapWorkstream, waits?: Map<string, string[]>): Map<string, number> {
  const all = indexSteps(ws)
  const depth = new Map<string, number>()
  let cyclic = false
  const of = (id: string, path: Set<string>): number => {
    const known = depth.get(id)
    if (known !== undefined) return known
    const step = all.get(id)
    if (!step || step.superseded) return -1
    if (path.has(id)) { cyclic = true; return -1 }
    path.add(id)
    const d = 1 + Math.max(-1, ...(waits?.get(id) ?? step.blocked_by ?? []).map((b) => of(b, path)))
    path.delete(id)
    depth.set(id, d)
    return d
  }
  for (const id of all.keys()) of(id, new Set())
  // Container cycles have no vertical solution: retain their plan order.
  if (waits && cyclic) for (const id of waits.keys()) depth.set(id, 0)
  return depth
}

export const SPINE_TASKS = 3 // open beads shown inline on a row
export const SPINE_HEAD = 46 // a row's title line
export const MARKER_H = 18 // one "blocked by" line
export const SPINE_LINE = 18 // one inline bead, the "+N more" line, or the guards line

export interface StageNode {
  id: string
  gate: boolean
  track?: boolean // a track of the workstream: always a stage
  blockedBy: string[] // ids among the workstream's own steps
}

/**
 * spineStageIds picks the lifecycle stages among a workstream's steps: the
 * gates and tracks, whatever is tied to a gate by a blocks edge, and open steps tied to
 * those. The rest (a plan, a spike, a unit) is history, folded into a stage.
 * Mirrors stageIDs in internal/livedata/viz.go.
 */
export function spineStageIds(nodes: StageNode[], closed: (id: string) => boolean): Set<string> {
  const stages = new Set(nodes.filter((n) => n.gate || n.track).map((n) => n.id))
  const touches = (n: StageNode, set: Set<string>) => n.blockedBy.some((b) => set.has(b)) || nodes.some((m) => set.has(m.id) && m.blockedBy.includes(n.id))
  const gates = new Set(stages)
  for (const n of nodes) if (touches(n, gates)) stages.add(n.id)
  for (let grew = true; grew; ) {
    grew = false
    for (const n of nodes) {
      if (stages.has(n.id) || closed(n.id) || !touches(n, stages)) continue
      stages.add(n.id)
      grew = true
    }
  }
  return stages
}

/** spineHistoryHome is the stage that takes the old done history: the first finished non-gate stage ("Build done"), else the first non-gate one, else the first. */
export function spineHistoryHome(order: { id: string; gate: boolean; closed: boolean }[]): string | undefined {
  return (order.find((n) => !n.gate && n.closed) ?? order.find((n) => !n.gate) ?? order[0])?.id
}

/** levelOf is the Graph level of a step: the snapshot's viz (the viz:<n> label, else gate 1, stage 2, task 3). */
export function levelOf(s: Pick<SnapStep, 'viz' | 'gate'>, stage: boolean): GraphLevel {
  return s.viz ?? (s.gate ? 1 : stage ? 2 : 3)
}

export interface FlatStep {
  step: SnapStep
  parent?: string // bead id of the step it sits under
  level: GraphLevel
}

/** flattenSteps lists the workstream's steps at every depth (parents first) with the level each is drawn at. */
export function flattenSteps(ws: SnapWorkstream): FlatStep[] {
  const top = ws.steps.filter((s) => !s.superseded)
  const ids = new Set(top.map((s) => s.bead_id))
  const byId = new Map(top.map((s) => [s.bead_id, s]))
  const stages = spineStageIds(
    top.map((s) => ({ id: s.bead_id, gate: !!s.gate, track: !!s.track, blockedBy: (s.blocked_by ?? []).filter((b) => ids.has(b)) })),
    (id) => byId.get(id)?.status === 'closed',
  )
  const out: FlatStep[] = []
  const walk = (list: SnapStep[], parent?: string) =>
    list.filter((s) => !s.superseded).forEach((s) => {
      out.push({ step: s, parent, level: levelOf(s, !parent && stages.has(s.bead_id)) })
      walk(s.children ?? [], s.bead_id)
    })
  walk(top)
  return out
}

const rank = (s: SnapStep) => (s.status === 'deferred' ? 3 : s.state === 'needs-user' ? 0 : s.state === 'in-progress' || s.state === 'review' || s.state === 'blocked' ? 1 : 2)

// attached splits the beads hanging off a row: open ones inline (needs you
// first, then under way, then not started), the done ones as a count or,
// expanded, listed after them.
function attached(tasks: SnapStep[], expanded: boolean, guards?: string): SpineInfo {
  const task = (s: SnapStep): SpineTask => ({ id: s.bead_id, title: s.title, state: s.state, status: s.status })
  const open = tasks.filter((t) => t.status !== 'closed').sort((a, b) => rank(a) - rank(b))
  const done = tasks.filter((t) => t.status === 'closed')
  const shown = expanded ? open : open.slice(0, SPINE_TASKS)
  return { tasks: [...shown, ...(expanded ? done : [])].map(task), done: done.length, more: open.length - shown.length, expanded, guards }
}

const GATE_KIND_OF_VERIFIER = (v: string): GateKind => (v === 'user' ? 'yours' : v === 'auto' ? 'auto' : v.startsWith('reviewer') ? 'reviewer' : 'operator')

// gateNodeCard is a stage gate bead (a check on a stage) as a row.
function gateNodeCard(ws: SnapWorkstream, n: SnapGateNode, stageTitle: string | undefined, expanded: boolean): Item {
  const reached = stageReached(ws, n.stage)
  const login = n.verifier.startsWith('reviewer:') ? n.verifier.slice('reviewer:'.length) : ''
  const card: Omit<Card, 'x' | 'y' | 'w' | 'h'> = {
    id: `${wsKey(ws)}:${n.bead_id}`,
    title: n.name,
    state: n.status === 'passed' ? 'done' : n.status === 'failed' ? 'blocked' : reached ? 'in-progress' : 'not-started',
    status: n.status === 'passed' ? 'closed' : 'open',
    gate: true,
    gateNode: n.status,
    approvers: n.verifier === 'user' ? ['user'] : login ? [login] : undefined,
    tone: checkTone(n.status, n.verifier, reached),
    gateKind: GATE_KIND_OF_VERIFIER(n.verifier),
    detail: n.check || n.evidence || undefined,
    wsKey: wsKey(ws),
    beadId: n.bead_id,
    project: ws.project,
    spine: { ...attached([], expanded, stageTitle) },
  }
  return { kind: 'card', card }
}

/**
 * rowItems is a workstream below the full tree: one row per bead at this level
 * or above (a bead's level is data: the snapshot's viz), each carrying the
 * beads hidden under it. A hidden bead hangs off its nearest row above it;
 * history with no row above goes to the home stage (Stages), or to a row it
 * has a blocks edge with (Gates); the rest is counted on the workstream. null
 * when Stages has no row to show: the full tree is drawn then.
 */
function rowItems(ws: SnapWorkstream, level: 1 | 2, expanded: Set<string>): { items: Item[]; hidden: number } | null {
  const flat = flattenSteps(ws)
  const shown = flat.filter((f) => f.level <= level)
  const nodes = (ws.gate_nodes ?? []).filter((n) => (n.viz ?? 1) <= level)
  if (level === 2 && !shown.length && !nodes.length) return null
  const rowIds = new Set(shown.map((f) => f.step.bead_id))
  const all = indexSteps(ws)
  const key = (id: string) => `${wsKey(ws)}:${id}`
  const expandedIn = (id: string) => expanded.has(id)

  // Rows by how far down the work they sit (plan order within a depth), a stage's gates right after it.
  const waits = containerWaits(ws, shown.map((f) => f.step))
  const depth = workDepth(ws, waits)
  const titleOf = (id: string) => all.get(id)?.title ?? (id === ws.bead_id ? ws.title : undefined)
  const nodesOf = (stage: string) => nodes.filter((n) => n.stage === stage).map((n) => gateNodeCard(ws, n, titleOf(n.stage), expandedIn(n.bead_id)))
  const groups: { depth: number; items: Item[] }[] = []
  for (const f of flat) {
    const items: Item[] = []
    if (rowIds.has(f.step.bead_id)) {
      const s = f.step
      items.push({ kind: 'card', card: { ...beadOf(ws, s, ws.manager_role, false), ask: !!s.decision || s.state === 'needs-user' }, waits: (s.blocked_by ?? []).map(key), orderWaits: (waits.get(s.bead_id) ?? []).map(key) })
    }
    items.push(...nodesOf(f.step.bead_id))
    if (items.length) groups.push({ depth: depth.get(f.step.bead_id) ?? 0, items })
  }
  const rows: Item[] = [...nodesOf(ws.bead_id), ...groups.sort((a, b) => a.depth - b.depth).flatMap((g) => g.items)]
  const order = dependencyOrder(rows)
  const cards = order.flatMap((i) => (i.kind === 'card' ? [i] : []))

  // Where each hidden bead hangs.
  const parentOf = new Map(flat.map((f) => [f.step.bead_id, f.parent]))
  const nearestRow = (id: string) => {
    for (let p = parentOf.get(id); p; p = parentOf.get(p)) if (rowIds.has(p)) return p
    return undefined
  }
  const home = level === 2 ? spineHistoryHome(cards.filter((i) => !i.card.gateNode).map((i) => ({ id: i.card.beadId!, gate: !!i.card.gate, closed: i.card.status === 'closed' }))) : undefined
  // The row a hidden bead is tied to by a blocks edge: one it waits on first, else one waiting on it; ties by id, so plan order never matters.
  const natural = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true })
  const neighbour = (id: string) => {
    const mine = all.get(id)?.blocked_by ?? []
    const before = shown.filter((r) => mine.includes(r.step.bead_id)).map((r) => r.step.bead_id)
    const after = shown.filter((r) => r.step.blocked_by?.includes(id)).map((r) => r.step.bead_id)
    return (before.sort(natural)[0] ?? after.sort(natural)[0]) as string | undefined
  }
  const hung = new Map<string, SnapStep[]>()
  let hidden = 0
  for (const f of flat) {
    if (f.level <= level) continue
    let row = nearestRow(f.step.bead_id) ?? home
    for (let id: string | undefined = f.step.bead_id; !row && level === 1 && id; id = parentOf.get(id)) row = neighbour(id)
    if (row) hung.set(row, [...(hung.get(row) ?? []), f.step])
    else hidden++
  }
  for (const i of cards) {
    if (i.card.gateNode) continue
    i.card.spine = attached(hung.get(i.card.beadId!) ?? [], expandedIn(i.card.beadId!))
  }
  arrange(order, true, markerOf(ws, all, rowIds))
  return { items: order, hidden }
}

export interface CanvasOpts {
  level?: GraphLevel // 1 gates only, 2 stages, 3 (default) every bead
  expanded?: Set<string> // row bead ids whose hidden beads are all listed
}

function workstreamItem(ws: SnapWorkstream, opts: CanvasOpts = {}): Item {
  const level = opts.level ?? 3
  const rows = level < 3 ? rowItems(ws, level as 1 | 2, opts.expanded ?? new Set()) : null
  let children: Item[]
  if (rows) children = rows.items
  else {
    const all = indexSteps(ws)
    const depth = workDepth(ws, containerWaits(ws, ws.steps))
    const beadOf = (i: Item) => (i.kind === 'card' ? i.card.beadId : i.box.beadId) ?? ''
    const top = stepItems(ws, ws.steps, ws.manager_role, all).sort((a, b) => (depth.get(beadOf(a)) ?? 0) - (depth.get(beadOf(b)) ?? 0))
    children = arrange(dependencyOrder(top), true, markerOf(ws, all))
  }
  const crew = [...new Set([...(ws.manager_role ? [ws.manager_role] : []), ...crewOf(children)])]
  return {
    kind: 'box',
    box: { id: wsKey(ws), kind: 'workstream', title: ws.title, state: ws.state, status: ws.status, review: ws.review, agent: ws.manager_role, wsKey: wsKey(ws), beadId: ws.bead_id, project: ws.project, crew, level: rows ? level : 3, hidden: rows?.hidden },
    children,
    sequence: true,
    cols: 1,
  }
}

// headH is the room a box's head takes: title, state line, gates checklist, and the hidden-beads line.
export function headH(b: Box): number {
  return BOX_HEAD + gatesH(b, GATE_CHARS_BOX) + (b.hidden ? MARKER_H : 0) + (b.markers?.length ?? 0) * MARKER_H
}

function size(item: Item): { w: number; h: number } {
  if (item.kind === 'card' && item.card.spine) {
    const sp = item.card.spine
    const lines = sp.tasks.length + (sp.more || sp.done || sp.expanded ? 1 : 0) + (sp.guards ? 1 : 0)
    return { w: BAND_W, h: SPINE_HEAD + (item.card.markers?.length ?? 0) * MARKER_H + lines * SPINE_LINE + 8 }
  }
  if (item.kind === 'card') {
    const markers = (item.card.markers?.length ?? 0) * MARKER_H
    return item.card.gate ? { w: BAND_W, h: gateBandH(item.card.title, item.card.inputs?.length) } : { w: CARD_W, h: CARD_H + gatesH(item.card) + markers }
  }
  const head = headH(item.box as Box)
  const sizes = item.children.map(size)
  if (!sizes.length) return { w: CARD_W + PAD * 2, h: head + PAD }
  if (item.sequence) {
    // A box is wide enough for its title and its agent's tag.
    const w = Math.max(WS_MIN_W, ...sizes.map((s) => s.w))
    const h = sizes.reduce((a, s) => a + s.h, 0) + GAP_SEQ * (sizes.length - 1)
    return { w: w + PAD * 2, h: head + h + PAD }
  }
  // Parallel parts: a grid of `cols` columns.
  const n = Math.min(item.cols, sizes.length)
  const colW = Math.max(...sizes.map((s) => s.w))
  const rows: number[] = []
  sizes.forEach((s, i) => (rows[Math.floor(i / n)] = Math.max(rows[Math.floor(i / n)] ?? 0, s.h)))
  const w = colW * n + GAP_PAR * (n - 1)
  const h = rows.reduce((a, r) => a + r, 0) + GAP_PAR * (rows.length - 1)
  return { w: w + PAD * 2, h: head + h + PAD }
}

/** buildCanvas lays out the whole fleet (Commander, as a grid of workstreams) or one workstream. */
export function buildCanvas(snap: Snapshot, focus?: string, cols = 3, opts: CanvasOpts = {}): Canvas {
  const cards: Card[] = []
  const boxes: Box[] = []
  const edges: Edge[] = []

  const place = (item: Item, x: number, y: number): { w: number; h: number } => {
    const sz = size(item)
    for (const w of item.waits ?? []) edges.push({ from: w, to: item.kind === 'card' ? item.card.id : item.box.id })
    if (item.kind === 'card') {
      cards.push({ ...item.card, x, y, ...sz })
      return sz
    }
    const box = { ...item.box, x, y, w: sz.w, h: sz.h } as Box
    boxes.push(box)
    const innerW = sz.w - PAD * 2
    let cy = y + headH(box)
    if (item.sequence) {
      // Steps stacked, centred so an arrow between neighbours runs straight down.
      for (const c of item.children) {
        const s = size(c)
        place(c, x + PAD + (innerW - s.w) / 2, cy)
        cy += s.h + GAP_SEQ
      }
    } else {
      const sizes = item.children.map(size)
      const n = Math.min(item.cols, sizes.length)
      const colW = Math.max(...sizes.map((s) => s.w))
      let rowH = 0
      item.children.forEach((c, i) => {
        const col = i % n
        if (col === 0 && i > 0) {
          cy += rowH + GAP_PAR
          rowH = 0
        }
        place(c, x + PAD + col * (colW + GAP_PAR), cy)
        rowH = Math.max(rowH, sizes[i].h)
      })
    }
    return sz
  }

  const one = focus ? snap.workstreams.find((w) => wsKey(w) === focus) : undefined
  if (one) place(workstreamItem(one, opts), MARGIN, MARGIN)
  else {
    let x = MARGIN
    let y = MARGIN
    let rowH = 0
    snap.workstreams.forEach((ws, i) => {
      if (i > 0 && i % cols === 0) {
        x = MARGIN
        y += rowH + GAP_GRID
        rowH = 0
      }
      const s = place(workstreamItem(ws, opts), x, y)
      x += s.w + GAP_GRID
      rowH = Math.max(rowH, s.h)
    })
  }

  // Only arrows with both ends on screen. Every arrow is from the bead right
  // above (arrange), so it runs straight down and crosses nothing.
  const ids = new Set([...cards.map((c) => c.id), ...boxes.map((b) => b.id)])
  const shown = edges.filter((e) => ids.has(e.from) && ids.has(e.to))
  // 0 first: with no workstreams Math.max() is -Infinity, and the pane's fit loops on it.
  const width = Math.max(0, ...cards.map((c) => c.x + c.w), ...boxes.map((b) => b.x + b.w)) + MARGIN
  const height = Math.max(0, ...cards.map((c) => c.y + c.h), ...boxes.map((b) => b.y + b.h)) + MARGIN
  return { cards, boxes, edges: shown, width, height }
}
