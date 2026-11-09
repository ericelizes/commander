// Graph == beads: the Graph's model against br data, on the real shapes
// (example-app t04, t03, t01) and on hand-made cases for each mismatch kind.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Snapshot, SnapStep } from '../src/shared/snapshot'
import { actualGraph, checkSnapshot, compareArrows, compareOrder, compareGraph, compareRows, expectedGraph, expectedRows, type BrBead } from '../src/renderer/src/graphCheck'
import { buildCanvas } from '../src/renderer/src/graph'
import { wsKey } from '../src/renderer/src/viewmodel'

interface Real {
  snapshot: Snapshot
  br: Record<string, BrBead[]>
}
const real = JSON.parse(readFileSync(join(__dirname, 'fixtures/graph-sample.json'), 'utf8')) as Real
const clone = <T,>(x: T): T => JSON.parse(JSON.stringify(x))
const kinds = (snap: Snapshot, br = real.br, only?: string) => checkSnapshot(snap, br, only).flatMap((r) => r.mismatches.map((m) => `${m.kind}:${m.bead}`))
const find = (steps: SnapStep[], id: string): SnapStep | undefined => steps.flatMap((s) => [s, ...(s.children ?? [])]).find((s) => s.bead_id === id) ?? steps.flatMap((s) => (s.children ? [find(s.children, id)] : [])).find(Boolean)

describe('Graph == beads on real data', () => {
  it('has no mismatch for t04, t03 and t01', () => {
    expect(checkSnapshot(real.snapshot, real.br)).toHaveLength(3)
    expect(kinds(real.snapshot)).toEqual([])
  })

  it('draws a gate with children as a band whose inputs are muted when closed (t04.6)', () => {
    const t04 = real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t04')!
    const c = buildCanvas(real.snapshot, wsKey(t04))
    const gate = c.cards.find((x) => x.beadId === 'example-app-t04.6')!
    expect(gate.gate).toBe(true)
    expect(gate.inputs?.map((i) => i.id).sort()).toEqual(['example-app-t04.1', 'example-app-t04.3.1', 'example-app-t04.6.1'])
    expect(gate.inputs!.every((i) => i.muted)).toBe(true)
  })

  it('nests a bead moved under another by its parent-child edge (t04.3.1 sits under t04, not t04.3)', () => {
    const t04 = real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t04')!
    const exp = expectedGraph(real.br['example-app'], t04.bead_id)
    expect(exp.nodes.get('example-app-t04.3.1')!.parent).toBe('example-app-t04')
  })
})

describe('compareGraph names every kind of mismatch', () => {
  const t04 = real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t04')!
  const only = (snap: Snapshot) => kinds(snap, real.br, 'example-app').filter((k) => k.includes('t04'))

  it('a node status that is not the bead\'s own', () => {
    const s = clone(real.snapshot)
    const step = find(s.workstreams.find((w) => w.bead_id === t04.bead_id)!.steps, 'example-app-t04.4')!
    step.state = 'done'
    expect(only(s)).toContain('status:example-app-t04.4')
    step.state = 'not-started'
    step.status = 'in_progress'
    expect(only(s)).toContain('status:example-app-t04.4')
  })

  it('an arrow that is not a dependency, and one that is missing', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === t04.bead_id)!
    find(ws.steps, 'example-app-t04.4')!.blocked_by = []
    expect(only(s)).toContain('edge-missing:example-app-t04.4')
    find(ws.steps, 'example-app-t04.4')!.blocked_by = ['example-app-t04.3', 'example-app-t04.1']
    expect(only(s)).toContain('edge-extra:example-app-t04.4')
  })

  it('a bead drawn in the wrong box', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === t04.bead_id)!
    const three = find(ws.steps, 'example-app-t04.3')!
    const four = ws.steps.find((x) => x.bead_id === 'example-app-t04.4')!
    ws.steps = ws.steps.filter((x) => x !== four)
    three.children = [...(three.children ?? []), four]
    expect(only(s)).toContain('nesting:example-app-t04.4')
  })

  it('a gate not drawn as a band, a lost input, an input not muted', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === t04.bead_id)!
    find(ws.steps, 'example-app-t04.6')!.gate = false
    expect(only(s)).toContain('gate-not-band:example-app-t04.6')
    const exp = expectedGraph(real.br['example-app'], t04.bead_id)
    const act = actualGraph(buildCanvas(real.snapshot, wsKey(t04)), t04)
    act.nodes.get('example-app-t04.6')!.inputs = ['example-app-t04.1']
    expect(compareGraph(exp, act, t04.bead_id).map((m) => m.kind)).toContain('gate-input-missing')
    const act2 = actualGraph(buildCanvas(real.snapshot, wsKey(t04)), t04)
    act2.nodes.get('example-app-t04.6')!.muted['example-app-t04.1'] = false
    expect(compareGraph(exp, act2, t04.bead_id).map((m) => m.kind)).toContain('input-not-muted')
  })

  it('a bead that is not drawn at all', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === t04.bead_id)!
    ws.steps = ws.steps.filter((x) => x.bead_id !== 'example-app-t04.4')
    expect(only(s)).toContain('missing-node:example-app-t04.4')
  })
})

describe('order and truth (il9.15.85)', () => {
  const ws = real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t01')!
  const at = (snap: Snapshot, level: 1 | 2 | 3) => buildCanvas(snap, wsKey(ws), 3, { level })
  const order = (level: 1 | 2 | 3, snap = real.snapshot) => at(snap, level).cards.map((c) => c.beadId)

  it('Workflow approved comes right after Spec approval at every level, before what it blocks', () => {
    expect(order(1).slice(0, 2)).toEqual(['example-app-t01.1', 'example-app-t01.21'])
    expect(order(2).slice(0, 2)).toEqual(['example-app-t01.1', 'example-app-t01.21'])
    const all = order(3)
    expect(all.indexOf('example-app-t01.21')).toBeLessThan(all.indexOf('example-app-t01.3'))
    expect(all.indexOf('example-app-t01.21')).toBeLessThan(all.indexOf('example-app-t01.17'))
  })

  it('the check names a bead drawn above its blocker', () => {
    const c = at(real.snapshot, 3)
    for (const x of c.cards) if (x.beadId === 'example-app-t01.21') x.y = 9999
    expect(compareOrder(c, real.br['example-app']).map((m) => m.bead)).toEqual(expect.arrayContaining(['example-app-t01.3']))
    expect(compareOrder(at(real.snapshot, 3), real.br['example-app'])).toEqual([])
  })

  it('a closed gate under a finished stage is folded away, not drawn approved (Prod deploy approval, Pilot sign-off)', () => {
    for (const level of [1, 2, 3] as const) expect(order(level)).not.toContain('example-app-t01.19')
  })

  it('a superseded gate is not drawn at any level and never reads as approved', () => {
    const s = clone(real.snapshot)
    const w = s.workstreams.find((x) => x.bead_id === 'example-app-t01')!
    w.steps.push({ ...clone(find(w.steps, 'example-app-t01.18')!), bead_id: 'example-app-t01.19', title: 'Prod deploy approval', status: 'closed', state: 'done', blocked_by: undefined, superseded: true })
    for (const level of [1, 2, 3] as const) expect(order(level, s)).not.toContain('example-app-t01.19')
  })
})

describe('graph levels', () => {
  const at = (bead: string, level: 1 | 2 | 3, expanded = new Set<string>(), snap = real.snapshot) => {
    const ws = snap.workstreams.find((w) => w.bead_id === bead)!
    return buildCanvas(snap, wsKey(ws), 3, { level, expanded })
  }
  const row = (c: ReturnType<typeof at>, id: string) => c.cards.find((x) => x.beadId === id)!
  const ids = (c: ReturnType<typeof at>) => c.cards.map((x) => x.beadId)

  it('Gates draws t01 as its gate beads only, in order, one arrow where a gate follows its prerequisite', () => {
    const c = at('example-app-t01', 1)
    expect(ids(c)).toEqual(['example-app-t01.1', 'example-app-t01.21', 'example-app-t01.17', 'example-app-t01.18'])
    expect(c.edges.map((e) => e.from.split(':').pop() + '>' + e.to.split(':').pop())).toEqual(['example-app-t01.17>example-app-t01.18'])
    expect(row(c, 'example-app-t01.17').spine!.more + row(c, 'example-app-t01.17').spine!.tasks.length).toBeGreaterThan(0)
  })

  it('Stages draws t01 as its stages in lifecycle order; done tasks collapse to a count', () => {
    const c = at('example-app-t01', 2)
    expect(ids(c)).toEqual(['example-app-t01.1', 'example-app-t01.21', 'example-app-t01.2', 'example-app-t01.17', 'example-app-t01.18', 'example-app-t01.3', 'example-app-t01.4'])
    const sp = row(c, 'example-app-t01.17').spine!
    expect(sp.done).toBe(11)
    expect(sp.tasks.filter((t) => t.status !== 'closed')).toHaveLength(3)
    expect(sp.tasks[0].id).toBe('example-app-t01.17.5') // under way comes first
    expect(sp.more).toBe(1)
    expect(sp.tasks.some((t) => t.status === 'closed')).toBe(false)
    expect(row(c, 'example-app-t01.2').spine!.done).toBe(12) // the old done steps fold into Build
  })

  it('a blocker that is not the bead right above becomes a marker, not an arrow', () => {
    const c = at('example-app-t01', 2)
    const live = row(c, 'example-app-t01.3') // waits on .21 (far above) and .18 (right above)
    expect(live.markers!.map((m) => [m.id, m.closed])).toEqual([['example-app-t01.21', true]])
    expect(c.edges.map((e) => e.to.split(':').pop())).toContain('example-app-t01.3')
  })

  it('lists every hidden bead once a row is expanded', () => {
    const sp = row(at('example-app-t01', 2, new Set(['example-app-t01.17'])), 'example-app-t01.17').spine!
    expect(sp.tasks).toHaveLength(15)
    expect(sp.more).toBe(0)
  })

  it('All is the full tree', () => {
    expect(at('example-app-t01', 3).cards.some((c) => c.spine)).toBe(false)
  })

  it('the level is data: viz pins a bead to a level', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === 'example-app-t01')!
    find(ws.steps, 'example-app-t01.2')!.viz = 1 // a stage pinned to Gates
    find(ws.steps, 'example-app-t01.18')!.viz = 3 // a gate pinned to All
    find(ws.steps, 'example-app-t01.17.5')!.viz = 2 // a task promoted to Stages
    expect(ids(at('example-app-t01', 1, new Set(), s))).toContain('example-app-t01.2')
    expect(ids(at('example-app-t01', 1, new Set(), s))).not.toContain('example-app-t01.18')
    expect(ids(at('example-app-t01', 2, new Set(), s))).toContain('example-app-t01.17.5')
    expect(ids(at('example-app-t01', 2, new Set(), s))).not.toContain('example-app-t01.18')
    expect(ids(at('example-app-t01', 3, new Set(), s)).filter((x) => x === 'example-app-t01.18')).toHaveLength(1)
  })

  it('a stage gate bead (gate_nodes) is a row at Gates, with its stage and state', () => {
    const s = clone(real.snapshot)
    const ws = s.workstreams.find((w) => w.bead_id === 'example-app-t01')!
    ws.gate_nodes = [{ kind: 'gate', bead_id: 'example-app-t01.2.g1', stage: 'example-app-t01.2', name: 'tests green', verifier: 'auto', status: 'open', blocked_by: ['example-app-t01.2'], blocks: [], viz: 1 }]
    const g = row(at('example-app-t01', 1, new Set(), s), 'example-app-t01.2.g1')
    expect(g.gateNode).toBe('open')
    expect(g.spine!.guards).toBe('Build')
    expect(g.gateKind).toBe('auto')
    expect(ids(at('example-app-t01', 1, new Set(), s)).indexOf('example-app-t01.2.g1')).toBeGreaterThan(0)
  })

  it('counts what no row carries on the workstream', () => {
    const c = at('example-app-t01', 1)
    expect(c.boxes[0].hidden).toBeGreaterThan(0)
    expect(at('example-app-t01', 2).boxes[0].hidden).toBe(0)
  })

  it('every arrow runs top to bottom and crosses nothing, at every level', () => {
    for (const bead of ['example-app-t01', 'example-app-t03', 'example-app-t04']) for (const level of [1, 2, 3] as const) expect(compareArrows(at(bead, level))).toEqual([])
  })

  it('the check catches an upward arrow and one through a bead', () => {
    const c = clone(at('example-app-t01', 2))
    const up = c.cards.find((x) => x.beadId === 'example-app-t01.18')!
    up.y = c.cards[0].y - 100
    c.edges = [{ from: c.cards.find((x) => x.beadId === 'example-app-t01.17')!.id, to: up.id }]
    expect(compareArrows(c).map((m) => m.kind)).toContain('arrow-direction')
    const d = clone(at('example-app-t01', 2))
    d.edges = [{ from: d.cards[0].id, to: d.cards[2].id }]
    expect(compareArrows(d).map((m) => m.kind)).toContain('arrow-crossing')
  })

  it('the check catches a wrong count, a hidden bead and a missing marker', () => {
    const exp = expectedRows(real.br['example-app'], 'example-app-t01', 2)
    const c = at('example-app-t01', 2)
    expect(compareRows(exp, c, real.br['example-app'], 'example-app-t01', wsKey(real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t01')!))).toEqual([])
    const bad = clone(c)
    const pr = row(bad, 'example-app-t01.17').spine!
    pr.done += 1
    pr.more = 0
    row(bad, 'example-app-t01.3').markers!.pop()
    bad.cards = bad.cards.filter((x) => x.beadId !== 'example-app-t01.4')
    const key = wsKey(real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t01')!)
    const kinds = compareRows(exp, bad, real.br['example-app'], 'example-app-t01', key).map((m) => `${m.kind}:${m.bead}`)
    expect(kinds).toContain('spine-hidden:example-app-t01.17')
    expect(kinds).toContain('spine-marker:example-app-t01.3')
    expect(kinds).toContain('spine-stage:example-app-t01.4')
  })

  it('Gates: the check sees a missing gate and a hidden count that is not the beads', () => {
    const key = wsKey(real.snapshot.workstreams.find((w) => w.bead_id === 'example-app-t04')!)
    const exp = expectedRows(real.br['example-app'], 'example-app-t04', 1)
    const c = at('example-app-t04', 1)
    expect(compareRows(exp, c, real.br['example-app'], 'example-app-t04', key)).toEqual([])
    const bad = clone(c)
    bad.boxes[0].hidden = 0
    expect(compareRows(exp, bad, real.br['example-app'], 'example-app-t04', key).map((m) => m.kind)).toContain('spine-hidden')
  })
})

describe('superseded and retired beads (il9.38.1)', () => {
  const frozen = JSON.parse(readFileSync(join(__dirname, 'fixtures/graph-retired.json'), 'utf8')) as { root: string; br: BrBead[] }
  const root = frozen.root

  it('expectedGraph leaves a superseded objective and its children out, a closed-reason one too', () => {
    const ids = [...expectedGraph(frozen.br, root).nodes.keys()]
    expect(ids).toEqual(expect.not.arrayContaining(['demo-ws.2', 'demo-ws.2.1', 'demo-ws.5']))
    expect(ids).toEqual(expect.arrayContaining(['demo-ws.1', 'demo-ws.3', 'demo-ws.4', 'demo-ws.6']))
  })

  it('a retired blocker stays a gate input and a blocked-by marker; real edges stay', () => {
    const exp = expectedGraph(frozen.br, root)
    // the renderer draws the gate's retired child (demo-ws.3.1) as an input too
    expect(exp.nodes.get('demo-ws.3')!.inputs).toEqual(['demo-ws.3.1', 'demo-ws.1', 'demo-ws.2'])
    expect([...exp.edges].sort()).toEqual(['demo-ws.1>demo-ws.3', 'demo-ws.2>demo-ws.3', 'demo-ws.2>demo-ws.4', 'demo-ws.3>demo-ws.4'])
  })

  it('closed-parent folding still holds: the children of the finished step are not nodes', () => {
    const ids = [...expectedGraph(frozen.br, root).nodes.keys()]
    expect(ids).not.toContain('demo-ws.1.1')
  })

  it('a subtree moved under another real parent follows the parent-child edge', () => {
    const exp = expectedGraph(frozen.br, root)
    expect(exp.nodes.get('demo-ws.6.1')!.parent).toBe('demo-ws.4')
  })

  it('expectedRows drops retired beads from rows and counts but keeps them as blockers', () => {
    const rows = expectedRows(frozen.br, root, 1)!
    expect([...rows.rows.keys()]).toEqual(['demo-ws.3'])
    expect(rows.rows.get('demo-ws.3')!.blockers).toEqual(new Set(['demo-ws.1', 'demo-ws.2']))
    const stages = expectedRows(frozen.br, root, 2)!
    expect([...stages.rows.keys()].sort()).toEqual(['demo-ws.1', 'demo-ws.3', 'demo-ws.4'])
    expect(stages.rows.get('demo-ws.4')!.blockers).toEqual(new Set(['demo-ws.2', 'demo-ws.3']))
    expect(stages.historyOpen).toBe(1) // demo-ws.6
    expect(stages.rows.get('demo-ws.4')!.open).toBe(1) // demo-ws.6.1, moved under it
    expect(stages.historyDone).toBe(0) // the retired demo-ws.3.1 and demo-ws.5 are not counted
  })

  it('a bead retired in both the snapshot and br raises no mismatch (example-app t04.4)', () => {
    const s = clone(real.snapshot)
    const br = clone(real.br)
    find(s.workstreams.find((w) => w.bead_id === 'example-app-t04')!.steps, 'example-app-t04.4')!.superseded = true
    br['example-app'].find((b) => b.id === 'example-app-t04.4')!.labels!.push('retired')
    expect(kinds(s, br, 'example-app').filter((k) => k.includes('t04'))).toEqual([])
  })

  // The snapshot as cmdrd builds it from br: nested by parent-child, superseded steps kept, a closed step's children folded away.
  const snapshotOf = (br: BrBead[]): { snap: Snapshot; ws: Snapshot['workstreams'][number] } => {
    const parentOf = (b: BrBead) => b.dependencies?.find((d) => d.dependency_type === 'parent-child')?.id
    const isRetired = (b: BrBead) => (b.labels ?? []).some((l) => l === 'retired' || l === 'superseded') || (b.status === 'closed' && (b.close_reason ?? '').toLowerCase().includes('superseded'))
    const step = (b: BrBead): SnapStep => ({
      bead_id: b.id,
      title: b.id,
      state: b.status === 'closed' ? 'done' : 'idle',
      status: b.status,
      gate: (b.labels ?? []).includes('gate') || undefined,
      superseded: isRetired(b) || undefined,
      blocked_by: b.dependencies?.filter((d) => d.dependency_type === 'blocks').map((d) => d.id),
      children: b.status === 'closed' ? [] : br.filter((c) => parentOf(c) === b.id).map(step),
    })
    const top = br.find((b) => b.id === frozen.root)!
    const ws = { ...clone(real.snapshot.workstreams[0]), project: 'demo', bead_id: top.id, steps: br.filter((c) => parentOf(c) === top.id).map(step) }
    return { snap: { ...clone(real.snapshot), workstreams: [ws] }, ws }
  }
  const mismatchesOf = (br: BrBead[]) => {
    const { snap, ws } = snapshotOf(br)
    const canvas = buildCanvas(snap, wsKey(ws), 3, { level: 3 })
    return compareGraph(expectedGraph(br, frozen.root), actualGraph(canvas, ws), frozen.root).map((m) => `${m.kind}:${m.bead}`)
  }

  it('the real renderer model against the validator: no mismatch on graph-retired', () => {
    expect(mismatchesOf(frozen.br)).toEqual([])
  })

  it('a non-retired child of a retired bead that blocks a gate is accepted as an input', () => {
    const br = clone(frozen.br)
    br.find((b) => b.id === 'demo-ws.3')!.dependencies!.push({ id: 'demo-ws.2.1', dependency_type: 'blocks' })
    expect(expectedGraph(br, frozen.root).nodes.get('demo-ws.3')!.inputs).toContain('demo-ws.2.1')
    expect(mismatchesOf(br)).toEqual([])
  })

  const cloneAct = (a: ReturnType<typeof actualGraph>) => ({ nodes: new Map([...a.nodes].map(([k, v]) => [k, clone(v)])), edges: new Set(a.edges) })

  it('a wrong expectation still reports a mismatch (input missing, extra marker)', () => {
    const exp = expectedGraph(frozen.br, root)
    const { snap, ws } = snapshotOf(frozen.br)
    const act = actualGraph(buildCanvas(snap, wsKey(ws), 3, { level: 3 }), ws)
    const missing = cloneAct(act)
    missing.nodes.get('demo-ws.3')!.inputs = ['demo-ws.1', 'demo-ws.2']
    expect(compareGraph(exp, missing, root).map((m) => m.kind)).toContain('gate-input-missing')
    const extra = cloneAct(act)
    extra.nodes.get('demo-ws.3')!.inputs.push('demo-ws.6')
    expect(compareGraph(exp, extra, root).map((m) => m.kind)).toContain('gate-input-extra')
    const marker = new Set(act.edges).add('demo-ws.2>demo-ws.6')
    expect(compareGraph(exp, { ...act, edges: marker }, root).map((m) => m.kind)).toContain('edge-extra')
    const dropped = new Set(act.edges)
    dropped.delete('demo-ws.2>demo-ws.4')
    expect(compareGraph(exp, { ...act, edges: dropped }, root).map((m) => m.kind)).toContain('edge-missing')
  })
})

describe('cross-track container order', () => {
  const fixture = () => {
    const snapshot = clone(real.snapshot)
    const ws = clone(snapshot.workstreams[0])
    ws.bead_id = 'sample-flow'
    ws.project = 'sample'
    const step = (id: string, children?: SnapStep[]): SnapStep => ({ bead_id: id, title: id, status: 'open', state: 'not-started', children })
    // The later track's child blocks the earlier track's child.
    const earlier = step('sample-flow.1', [step('sample-flow.1.1')])
    const later = step('sample-flow.2', [step('sample-flow.2.1')])
    earlier.track = later.track = true
    earlier.children![0].blocked_by = ['sample-flow.2.1']
    ws.steps = [earlier, later]
    ws.gate_nodes = []
    snapshot.workstreams = [ws]
    const beads: BrBead[] = [{ id: ws.bead_id, status: 'open', labels: ['kind:objective'] }]
    const walk = (steps: SnapStep[], parent: string) => steps.forEach((s) => {
      beads.push({ id: s.bead_id, status: s.status, labels: ['kind:objective', ...(s.track ? ['kind:track'] : [])], dependencies: [
        { id: parent, dependency_type: 'parent-child' },
        ...(s.blocked_by ?? []).map((id) => ({ id, dependency_type: 'blocks' })),
      ] })
      walk(s.children ?? [], s.bead_id)
    })
    walk(ws.steps, ws.bead_id)
    return { snapshot, ws, beads, earlier, later }
  }

  it('puts the blocking track first at All and Stages and preserves the child edge', () => {
    const { snapshot, ws, beads, earlier, later } = fixture()
    for (const level of [2, 3] as const) {
      const canvas = buildCanvas(snapshot, wsKey(ws), 3, { level })
      const drawn = [...canvas.cards, ...canvas.boxes]
      expect(drawn.find((n) => n.beadId === later.bead_id)!.y).toBeLessThan(drawn.find((n) => n.beadId === earlier.bead_id)!.y)
      expect(compareOrder(canvas, beads)).toEqual([])
      if (level === 3) expect(compareGraph(expectedGraph(beads, ws.bead_id), actualGraph(canvas, ws), ws.bead_id)).toEqual([])
    }
  })

  it('lifts hidden child dependencies at Gates and inside a nested box', () => {
    const { snapshot, ws, beads, earlier, later } = fixture()
    earlier.viz = later.viz = 1
    const gates = buildCanvas(snapshot, wsKey(ws), 3, { level: 1 })
    expect(gates.cards.map((c) => c.beadId)).toEqual([later.bead_id, earlier.bead_id])
    expect(compareOrder(gates, beads)).toEqual([])

    ws.steps = [{ bead_id: 'sample-flow.container', title: 'Container', status: 'open', state: 'not-started', children: ws.steps }]
    const nested = buildCanvas(snapshot, wsKey(ws))
    expect(nested.boxes.find((b) => b.beadId === later.bead_id)!.y).toBeLessThan(nested.boxes.find((b) => b.beadId === earlier.bead_id)!.y)
    expect(compareOrder(nested, beads)).toEqual([])
  })

  it('falls back to plan order for mutually blocking containers', () => {
    const { snapshot, ws, earlier, later } = fixture()
    later.children![0].blocked_by = ['sample-flow.1.1']
    for (const level of [2, 3] as const) {
      const canvas = buildCanvas(snapshot, wsKey(ws), 3, { level })
      const drawn = [...canvas.cards, ...canvas.boxes]
      expect(drawn.find((n) => n.beadId === earlier.bead_id)!.y).toBeLessThan(drawn.find((n) => n.beadId === later.bead_id)!.y)
    }
  })
})
