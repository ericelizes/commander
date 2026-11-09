// Graph == beads. expectedGraph is what the Graph must show for one workstream,
// worked out from br data alone; actualGraph reads what buildCanvas laid out;
// compareGraph lists every difference. Pure: runs in vitest and from
// scripts/graph-check.ts (cmdr graph check).

import { buildCanvas, SPINE_TASKS, spineStageIds, waitsOn, type Canvas } from './graph'
import type { GraphLevel } from '../../shared/snapshot'
import { wsKey } from './viewmodel'
import type { Snapshot, SnapWorkstream } from '../../shared/snapshot'

/** One bead as `br show --json` prints it. */
export interface BrBead {
  id: string
  title?: string
  status: string
  labels?: string[]
  close_reason?: string
  dependencies?: { id: string; dependency_type: string }[]
}

export interface ExpectedNode {
  id: string
  status: string
  parent?: string
  gate: boolean
  inputs: string[] // gate only: children and the beads it waits on, all drawn inside the band
  muted: Record<string, boolean>
}

export interface Expected {
  nodes: Map<string, ExpectedNode>
  edges: Set<string> // "from>to": `from` blocks `to`
}

const isWork = (b: BrBead) => (b.labels ?? []).some((l) => l === 'kind:objective' || l === 'kind:task')
const parentOf = (b: BrBead) => b.dependencies?.find((d) => d.dependency_type === 'parent-child')?.id
const retired = (b: BrBead) => (b.labels ?? []).some((l) => l === 'retired' || l === 'superseded') || (b.status === 'closed' && (b.close_reason ?? '').toLowerCase().includes('superseded'))
/** retiredUnder: dropped beads below `root` (any depth). They are never drawn, but a bead they block still names them. */
const retiredUnder = (kids: Map<string, BrBead[]>, root: string): Set<string> => {
  const out = new Set<string>()
  // Everything below a retired bead is dropped with it: the renderer's index holds every step at any depth.
  const walk = (id: string, gone: boolean) =>
    (kids.get(id) ?? []).filter(isWork).forEach((c) => {
      const dropped = gone || retired(c)
      if (dropped) out.add(c.id)
      walk(c.id, dropped)
    })
  walk(root, false)
  return out
}
const blockersOf = (b: BrBead) => (b.dependencies ?? []).filter((d) => d.dependency_type === 'blocks').map((d) => d.id)

/**
 * expectedGraph is the visible tree under a workstream bead: children by the
 * parent-child edge, a closed bead's children hidden (a finished step under
 * an open one stays), only objective and task beads. A gate (label `gate`)
 * is a band: its children and blockers are its inputs, not nodes of their own
 * unless they are siblings elsewhere.
 */
export function expectedGraph(beads: BrBead[], root: string): Expected {
  const byId = new Map(beads.map((b) => [b.id, b]))
  const kids = new Map<string, BrBead[]>()
  for (const b of beads) {
    const p = parentOf(b)
    if (p) kids.set(p, [...(kids.get(p) ?? []), b])
  }
  const nodes = new Map<string, ExpectedNode>()
  const gateOf = (b: BrBead) => (b.labels ?? []).includes('gate')
  const visit = (b: BrBead, parent?: string) => {
    const gate = gateOf(b)
    nodes.set(b.id, { id: b.id, status: b.status, parent, gate, inputs: [], muted: {} })
    if (b.status === 'closed' && parent !== undefined) return // a finished step's own children are folded away
    if (gate) return // a gate's children are inputs, filled in below
    for (const c of kids.get(b.id) ?? []) if (isWork(c) && !retired(c)) visit(c, b.id)
  }
  const rootBead = byId.get(root)
  if (!rootBead) return { nodes, edges: new Set() }
  visit(rootBead)
  const dropped = retiredUnder(kids, root)
  // Gates: inputs are the children (all depths, unless the gate is closed) and the blockers still in the tree.
  // The renderer takes a gate's retired children too (graph.ts descendants/gateInputs do not filter superseded).
  const deep = (id: string): BrBead[] => (kids.get(id) ?? []).filter(isWork).flatMap((c) => [c, ...deep(c.id)])
  const inGate = new Set<string>()
  for (const n of [...nodes.values()]) {
    if (!n.gate) continue
    const b = byId.get(n.id)!
    const own = b.status === 'closed' ? [] : deep(n.id)
    const waits = blockersOf(b).map((x) => byId.get(x)).filter((x): x is BrBead => !!x && (nodes.has(x.id) || dropped.has(x.id)))
    for (const i of [...own, ...waits]) {
      if (i.id === n.id || n.inputs.includes(i.id)) continue
      n.inputs.push(i.id)
      n.muted[i.id] = i.status === 'closed' || i.status === 'deferred'
    }
    for (const d of own) inGate.add(d.id)
  }
  // A gate's own descendants are not nodes: they are drawn inside it.
  for (const id of inGate) nodes.delete(id)
  const edges = new Set<string>()
  for (const n of nodes.values()) {
    // a retired blocker is not drawn, but the bead it blocks shows it as a marker
    for (const from of blockersOf(byId.get(n.id)!)) if (nodes.has(from) || dropped.has(from)) edges.add(`${from}>${n.id}`)
  }
  return { nodes, edges }
}

export interface ActualNode {
  id: string
  status?: string
  state: string
  parent?: string
  band: boolean
  inputs: string[]
  muted: Record<string, boolean>
}

export interface Actual {
  nodes: Map<string, ActualNode>
  edges: Set<string>
}

type Rect = { x: number; y: number; w: number; h: number }
const within = (r: Rect, b: Rect) => r.x >= b.x && r.y >= b.y && r.x + r.w <= b.x + b.w && r.y + r.h <= b.y + b.h
const area = (r: Rect) => r.w * r.h

/** actualGraph is what the canvas draws: nesting is geometric (which box a node sits inside). */
export function actualGraph(canvas: Canvas, ws: SnapWorkstream): Actual {
  const prefix = `${wsKey(ws)}:`
  const bead = (id: string) => (id === wsKey(ws) ? ws.bead_id : id.startsWith(prefix) ? id.slice(prefix.length) : id)
  const all = [...canvas.boxes.map((b) => ({ r: b as Rect, id: b.id })), ...canvas.cards.map((c) => ({ r: c as Rect, id: c.id }))]
  const parentOfRect = (id: string, r: Rect) => {
    const outer = canvas.boxes.filter((b) => b.id !== id && within(r, b) && area(b) > area(r)).sort((a, b) => area(a) - area(b))[0]
    return outer ? bead(outer.id) : undefined
  }
  const nodes = new Map<string, ActualNode>()
  for (const x of all) {
    const card = canvas.cards.find((c) => c.id === x.id)
    const item = card ?? canvas.boxes.find((b) => b.id === x.id)!
    const muted: Record<string, boolean> = {}
    for (const i of card?.inputs ?? []) muted[i.id] = i.muted
    nodes.set(bead(x.id), {
      id: bead(x.id),
      status: item.status,
      state: item.state,
      parent: parentOfRect(x.id, x.r),
      band: !!card?.gate,
      inputs: (card?.inputs ?? []).map((i) => i.id),
      muted,
    })
  }
  const edges = new Set(canvas.edges.map((e) => `${bead(e.from)}>${bead(e.to)}`))
  // A blocker not drawn as an arrow is a "blocked by" marker on the bead: the same edge, said in words.
  for (const x of [...canvas.cards, ...canvas.boxes]) for (const m of x.markers ?? []) edges.add(`${m.id}>${bead(x.id)}`)
  return { nodes, edges }
}

export interface Mismatch {
  bead?: string
  kind: 'order' | 'arrow-direction' | 'arrow-crossing' | 'spine-stage' | 'spine-count' | 'spine-hidden' | 'spine-marker' | 'missing-node' | 'extra-node' | 'status' | 'nesting' | 'edge-missing' | 'edge-extra' | 'gate-not-band' | 'gate-input-missing' | 'gate-input-extra' | 'input-not-muted'
  detail: string
}

// A workstream bead's own parent is not part of the view.
export function compareGraph(exp: Expected, act: Actual, root: string): Mismatch[] {
  const out: Mismatch[] = []
  const add = (kind: Mismatch['kind'], bead: string | undefined, detail: string) => out.push({ kind, bead, detail })
  for (const [id, e] of exp.nodes) {
    const a = act.nodes.get(id)
    if (!a) {
      add('missing-node', id, 'bead is in beads but not drawn')
      continue
    }
    if (a.status !== e.status) add('status', id, `drawn with status ${a.status ?? 'none'}, bead is ${e.status}`)
    // State is the bead's own status: closed is done and only closed is done.
    if ((e.status === 'closed') !== (a.state === 'done')) add('status', id, `drawn as ${a.state}, bead is ${e.status}`)
    if (id !== root && a.parent !== e.parent) add('nesting', id, `drawn inside ${a.parent ?? 'nothing'}, parent-child says ${e.parent ?? 'nothing'}`)
    if (e.gate && !a.band) add('gate-not-band', id, 'gate bead is not drawn as a band')
    if (!e.gate && a.band) add('gate-not-band', id, 'a band is drawn for a bead that is not a gate')
    if (e.gate) {
      for (const i of e.inputs) {
        if (!a.inputs.includes(i)) add('gate-input-missing', id, `input ${i} not listed in the band`)
        else if (e.muted[i] !== a.muted[i]) add('input-not-muted', id, `input ${i} is ${e.muted[i] ? 'closed but not muted' : 'open but muted'}`)
      }
      for (const i of a.inputs) if (!e.inputs.includes(i)) add('gate-input-extra', id, `input ${i} listed but is neither its child nor a bead it waits on`)
    }
  }
  for (const id of act.nodes.keys()) if (!exp.nodes.has(id)) add('extra-node', id, 'drawn but not a visible bead under the workstream')
  // A gate shows one arrow of many (the rest are its inputs); every other edge is drawn in full.
  const gateEdges = new Map<string, string[]>()
  for (const e of act.edges) {
    const [from, to] = e.split('>')
    if (exp.nodes.get(to)?.gate) gateEdges.set(to, [...(gateEdges.get(to) ?? []), from])
  }
  for (const e of exp.edges) {
    const to = e.split('>')[1]
    if (exp.nodes.get(to)?.gate) continue
    if (!act.edges.has(e)) add('edge-missing', to, `bead waits on ${e.split('>')[0]} but no arrow is drawn`)
  }
  for (const e of act.edges) {
    if (!exp.edges.has(e)) add('edge-extra', e.split('>')[1], `arrow ${e.replace('>', ' -> ')} is not a blocks dependency`)
  }
  for (const [to, froms] of gateEdges) if (froms.length > 1) add('edge-extra', to, `gate draws ${froms.length} arrows, the band lists the rest`)
  return out
}

// Arrows. Every arrow is a blocks edge drawn from the prerequisite down to
// what it unblocks; none may point up or pass through another bead.
export function compareArrows(canvas: Canvas): Mismatch[] {
  const out: Mismatch[] = []
  const rects = new Map<string, Rect>([...canvas.cards, ...canvas.boxes].map((x) => [x.id, x]))
  const inside = (r: Rect, b: Rect) => r.x >= b.x && r.y >= b.y && r.x + r.w <= b.x + b.w && r.y + r.h <= b.y + b.h
  for (const e of canvas.edges) {
    const a = rects.get(e.from)
    const b = rects.get(e.to)
    if (!a || !b) continue
    const name = `${e.from.split(':').pop()} -> ${e.to.split(':').pop()}`
    if (a.y + a.h > b.y) {
      out.push({ kind: 'arrow-direction', bead: e.to.split(':').pop(), detail: `arrow ${name} does not run top to bottom` })
      continue
    }
    // The drawn line: straight down from the middle of the prerequisite to the top of the bead it unblocks.
    const x = a.x + a.w / 2
    const y1 = a.y + a.h
    const y2 = b.y
    for (const [id, r] of rects) {
      if (id === e.from || id === e.to) continue
      if (inside(r, a) || inside(r, b)) continue // part of an endpoint
      if (inside(a, r) && inside(b, r)) continue // the box both live in
      if (x > r.x && x < r.x + r.w && y1 < r.y + r.h && y2 > r.y) out.push({ kind: 'arrow-crossing', bead: e.to.split(':').pop(), detail: `arrow ${name} runs through ${id.split(':').pop()}` })
    }
  }
  return out
}

// cyclicPairs: a container counts as blocked by whatever blocks any of its
// descendants, so a child feeding a sibling that blocks another child leaves
// no order that honours every edge. The renderer keeps plan order for such a
// group (graph.ts dropCycleEdges); an edge between two of its containers is not
// a mismatch. Same rule, evaluated per parent over br data.
function cyclicPairs(beads: BrBead[]): (from: string, to: string) => boolean {
  const byId = new Map(beads.map((b) => [b.id, b]))
  const kids = new Map<string, string[]>()
  for (const b of beads) {
    const p = parentOf(b)
    if (p && isWork(b)) kids.set(p, [...(kids.get(p) ?? []), b.id])
  }
  const chain = (id: string): string[] => {
    const out = [id]
    for (let p = parentOf(byId.get(id)!); p && byId.has(p) && out.length < 64; p = parentOf(byId.get(p)!)) out.push(p)
    return out
  }
  const lifted = new Map<string, Map<string, string[]>>() // parent -> child -> lifted blockers (children of that parent)
  const liftedAt = (parent: string) => {
    let m = lifted.get(parent)
    if (m) return m
    m = new Map((kids.get(parent) ?? []).map((c) => [c, [] as string[]]))
    const walk = (id: string, home: string) => {
      for (const from of blockersOf(byId.get(id)!)) {
        if (!byId.has(from)) continue
        const mine = chain(from).find((a) => m!.has(a) && parentOf(byId.get(a)!) === parent)
        if (mine && mine !== home) m!.get(home)!.push(mine)
      }
      for (const c of kids.get(id) ?? []) walk(c, home)
    }
    for (const c of m.keys()) walk(c, c)
    lifted.set(parent, m)
    return m
  }
  return (from, to) => {
    if (!byId.has(from) || !byId.has(to)) return false
    const up = chain(to)
    const common = chain(from).find((a) => up.includes(a) && a !== from && a !== to)
    const parent = common ?? (parentOf(byId.get(from)!) === parentOf(byId.get(to)!) ? parentOf(byId.get(to)!) : undefined)
    if (!parent) return false
    const pick = (list: string[]) => list[list.indexOf(parent) - 1]
    const a = pick(chain(from))
    const b = pick(up)
    if (!a || !b || a === b) return false
    return waitsOn(liftedAt(parent), a, b)
  }
}

// Order. The Graph reads in work order: a bead is never drawn above a bead
// that blocks it, whether or not an arrow joins them (a gate that blocks a
// stage no row of this level shows still comes first).
export function compareOrder(canvas: Canvas, beads: BrBead[]): Mismatch[] {
  const out: Mismatch[] = []
  const drawn = new Map([...canvas.cards, ...canvas.boxes].filter((x) => x.beadId).map((x) => [x.beadId!, x]))
  const unsatisfiable = cyclicPairs(beads)
  for (const b of beads) {
    const to = drawn.get(b.id)
    if (!to || b.status === 'deferred') continue // parked: out of play
    for (const from of blockersOf(b)) {
      const above = drawn.get(from)
      if (above && above.y > to.y && !unsatisfiable(from, b.id)) out.push({ kind: 'order', bead: b.id, detail: `drawn above ${from}, which blocks it` })
    }
  }
  return out
}

// Levels. A bead's level is data (label viz:<n>; else a gate is 1, a stage 2,
// a task 3). Gates shows level 1, Stages levels 1 and 2, All everything; a bead
// hidden at a level hangs off a row (open ones inline, a count for the rest) or
// is counted on the workstream.
export interface ExpectedRow {
  id: string
  gateNode: boolean
  open: number // hidden open beads hung off it
  done: number
  blockers: Set<string> // blocks edges into it, from beads of the workstream
}

export interface ExpectedRows {
  rows: Map<string, ExpectedRow>
  historyOpen: number // hidden beads that go to the home stage (Stages only)
  historyDone: number
  hidden: number // hidden beads no row carries
}

const unescapeLabel = (v: string) => v.replace(/_([0-9a-f]{2})/g, (_, h: string) => String.fromCharCode(parseInt(h, 16)))
const vizOfBr = (b: BrBead) => {
  const l = (b.labels ?? []).find((x) => /^viz:[123]$/.test(x))
  return l ? (Number(l.slice(4)) as GraphLevel) : undefined
}

/** expectedRows works the rows of one level out from br data alone; null when Stages has nothing to show (the full tree is drawn). */
export function expectedRows(beads: BrBead[], root: string, level: 1 | 2): ExpectedRows | null {
  const kids = new Map<string, BrBead[]>()
  for (const b of beads) {
    const p = parentOf(b)
    if (p) kids.set(p, [...(kids.get(p) ?? []), b])
  }
  const work = (id: string) => (kids.get(id) ?? []).filter((c) => isWork(c) && !retired(c))
  const closed = (b: BrBead) => b.status === 'closed'
  const isGate = (b: BrBead) => (b.labels ?? []).includes('gate')
  const top = work(root)
  const topIds = new Set(top.map((b) => b.id))
  const stageIds = spineStageIds(
    top.map((b) => ({ id: b.id, gate: isGate(b), track: (b.labels ?? []).includes('kind:track'), blockedBy: blockersOf(b).filter((x) => topIds.has(x)) })),
    (id) => closed(top.find((b) => b.id === id)!),
  )
  // Every bead of the tree, parents first; a finished bead's children are folded away.
  const flat: { b: BrBead; parent?: string; level: number }[] = []
  const walk = (list: BrBead[], parent?: string) =>
    list.forEach((b) => {
      flat.push({ b, parent, level: vizOfBr(b) ?? (isGate(b) ? 1 : !parent && stageIds.has(b.id) ? 2 : 3) })
      if (!closed(b)) walk(work(b.id), b.id)
    })
  walk(top)
  const inTree = new Set([root, ...flat.map((f) => f.b.id)])
  const markerIds = new Set([...inTree, ...retiredUnder(kids, root)]) // a dropped bead still shows as a blocker marker
  const gateNodes = beads.filter((b) => (b.labels ?? []).includes('kind:gate') && b.status !== 'tombstone' && !retired(b)).flatMap((b) => {
    const stage = (b.labels ?? []).find((l) => l.startsWith('gate-of:'))
    return stage && inTree.has(unescapeLabel(stage.slice('gate-of:'.length))) ? [{ b, level: vizOfBr(b) ?? 1 }] : []
  })
  const shown = flat.filter((f) => f.level <= level)
  const nodes = gateNodes.filter((g) => g.level <= level)
  if (level === 2 && !shown.length && !nodes.length) return null
  const rowIds = new Set(shown.map((f) => f.b.id))
  const rows = new Map<string, ExpectedRow>()
  for (const f of shown) rows.set(f.b.id, { id: f.b.id, gateNode: false, open: 0, done: 0, blockers: new Set(blockersOf(f.b).filter((x) => markerIds.has(x) && x !== root)) })
  for (const g of nodes) rows.set(g.b.id, { id: g.b.id, gateNode: true, open: 0, done: 0, blockers: new Set() })
  const parent = new Map(flat.map((f) => [f.b.id, f.parent]))
  const nearest = (id: string) => {
    for (let p = parent.get(id); p; p = parent.get(p)) if (rowIds.has(p)) return p
    return undefined
  }
  const byId = new Map(flat.map((f) => [f.b.id, f.b]))
  const natural = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true })
  const neighbour = (id: string) => {
    const mine = blockersOf(byId.get(id)!)
    const before = shown.filter((r) => mine.includes(r.b.id)).map((r) => r.b.id)
    const after = shown.filter((r) => blockersOf(r.b).includes(id)).map((r) => r.b.id)
    return before.sort(natural)[0] ?? after.sort(natural)[0]
  }
  const out: ExpectedRows = { rows, historyOpen: 0, historyDone: 0, hidden: 0 }
  for (const f of flat) {
    if (f.level <= level) continue
    let row = nearest(f.b.id)
    if (!row && level === 2) {
      closed(f.b) ? out.historyDone++ : out.historyOpen++
      continue
    }
    for (let id: string | undefined = f.b.id; !row && id; id = parent.get(id)) row = neighbour(id)
    if (!row) {
      out.hidden++
      continue
    }
    const r = rows.get(row)!
    closed(f.b) ? r.done++ : r.open++
  }
  return out
}

/** compareRows checks a Gates or Stages canvas against br: the rows, what hangs off each, the blockers, and the hidden count. */
export function compareRows(exp: ExpectedRows | null, canvas: Canvas, beads: BrBead[], root: string, rootKey: string): Mismatch[] {
  const out: Mismatch[] = []
  const add = (kind: Mismatch['kind'], bead: string | undefined, detail: string) => out.push({ kind, bead, detail })
  const status = new Map(beads.map((b) => [b.id, b.status]))
  const acts = canvas.cards.filter((c) => c.spine)
  if (!exp) {
    for (const a of acts) add('spine-stage', a.beadId, 'a row is drawn but the workstream has nothing to show at this level')
    return out
  }
  const arrows = new Map<string, Set<string>>() // to -> from
  for (const e of canvas.edges) {
    const to = e.to.slice(rootKey.length + 1)
    arrows.set(to, new Set([...(arrows.get(to) ?? []), e.from.slice(rootKey.length + 1)]))
  }
  let extraOpen = 0
  let extraDone = 0
  for (const [id, e] of exp.rows) {
    const a = acts.find((x) => x.beadId === id)
    if (!a) {
      add('spine-stage', id, 'bead is at this level but has no row')
      continue
    }
    const sp = a.spine!
    const open = sp.tasks.filter((t) => t.status !== 'closed').length + sp.more
    // The home stage also takes the history, so a row may carry more than what hangs off it; never less.
    if (open < e.open) add('spine-hidden', id, `row accounts for ${open} open beads under it, beads have ${e.open}`)
    if (sp.done < e.done) add('spine-count', id, `row says ${sp.done} done, beads have ${e.done} closed under it`)
    extraOpen += Math.max(0, open - e.open)
    extraDone += Math.max(0, sp.done - e.done)
    if (!sp.expanded && sp.tasks.some((t) => t.status === 'closed')) add('spine-hidden', id, 'a done bead is drawn inline while collapsed')
    if (!sp.expanded && sp.tasks.length > SPINE_TASKS) add('spine-hidden', id, `${sp.tasks.length} beads inline, the cap is ${SPINE_TASKS}`)
    for (const t of sp.tasks.filter((t) => !sp.expanded || t.status !== 'closed')) if (status.get(t.id) === 'closed') add('spine-hidden', t.id, 'a closed bead is drawn as open')
    // Blocks edges into the row: an arrow from the bead above, or a marker.
    const got = new Set([...(arrows.get(id) ?? []), ...(a.markers ?? []).map((m) => m.id)])
    for (const m of e.blockers) if (!got.has(m)) add('spine-marker', id, `blocked by ${m} but the row shows neither an arrow nor a marker`)
    for (const m of got) if (!e.blockers.has(m)) add('spine-marker', id, `${m} is shown as blocking it but is not a blocks dependency`)
    for (const m of a.markers ?? []) {
      if (m.closed !== (status.get(m.id) === 'closed')) add('spine-marker', id, `marker ${m.id} is ${m.closed ? 'closed' : 'open'} in the row, not in beads`)
      if (!!m.hidden !== !exp.rows.has(m.id)) add('spine-marker', id, `marker ${m.id} is ${m.hidden ? 'hidden' : 'a row'} in the view, not at this level`)
    }
    for (const m of arrows.get(id) ?? []) if (!exp.rows.has(m)) add('spine-marker', id, `arrow from ${m}, which has no row`)
  }
  for (const a of acts) if (!exp.rows.has(a.beadId ?? '')) {
    add('spine-stage', a.beadId, 'row is drawn for a bead that is not at this level')
  }
  if (extraOpen !== exp.historyOpen) add('spine-hidden', undefined, `rows carry ${extraOpen} open beads beyond their own, beads have ${exp.historyOpen} open history`)
  if (extraDone !== exp.historyDone) add('spine-count', undefined, `rows carry ${extraDone} done beads beyond their own, beads have ${exp.historyDone} finished history`)
  const hidden = canvas.boxes.find((b) => b.beadId === root)?.hidden ?? 0
  if (hidden !== exp.hidden) add('spine-hidden', undefined, `workstream says ${hidden} beads are hidden under no row, beads have ${exp.hidden}`)
  return out
}

export interface WsResult {
  project: string
  bead_id: string
  title: string
  mismatches: Mismatch[]
}

const LEVEL_WORD = { 1: 'Gates', 2: 'Stages', 3: 'All' } as const

/** checkSnapshot runs the whole comparison at every level: the snapshot's workstreams against each project's br beads. */
export function checkSnapshot(snap: Snapshot, brByProject: Record<string, BrBead[]>, only?: string): WsResult[] {
  return snap.workstreams
    .filter((ws) => !only || ws.project === only)
    .map((ws) => {
      const beads = brByProject[ws.project] ?? []
      const key = wsKey(ws)
      const tag = (level: GraphLevel, ms: Mismatch[]) => ms.map((m) => ({ ...m, detail: `[${LEVEL_WORD[level]}] ${m.detail}` }))
      const all = buildCanvas(snap, key, 3, { level: 3 })
      const mismatches = [...tag(3, compareGraph(expectedGraph(beads, ws.bead_id), actualGraph(all, ws), ws.bead_id)), ...tag(3, compareArrows(all)), ...tag(3, compareOrder(all, beads))]
      for (const level of [1, 2] as const) {
        const canvas = buildCanvas(snap, key, 3, { level })
        mismatches.push(...tag(level, [...compareRows(expectedRows(beads, ws.bead_id, level), canvas, beads, ws.bead_id, key), ...compareArrows(canvas), ...compareOrder(canvas, beads)]))
      }
      return { project: ws.project, bead_id: ws.bead_id, title: ws.title, mismatches }
    })
}
