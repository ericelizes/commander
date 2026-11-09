import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Snapshot, SnapStep } from '../src/shared/snapshot'
import { compareOrder, type BrBead } from '../src/renderer/src/graphCheck'
import { buildCanvas } from '../src/renderer/src/graph'
import { wsKey } from '../src/renderer/src/viewmodel'

const real = JSON.parse(readFileSync(join(__dirname, 'fixtures/graph-sample.json'), 'utf8')) as { snapshot: Snapshot }
const clone = <T,>(x: T): T => JSON.parse(JSON.stringify(x))
const step = (id: string, children?: SnapStep[], blocked_by?: string[]): SnapStep => ({ bead_id: id, title: id, status: 'open', state: 'not-started', children, blocked_by })

// build wraps the given top-level steps in a workstream and returns its beads as br would list them.
function build(steps: SnapStep[]) {
  const snapshot = clone(real.snapshot)
  const ws = clone(snapshot.workstreams[0])
  ws.bead_id = 'sample-flow'
  ws.project = 'sample'
  ws.steps = steps
  ws.gate_nodes = []
  snapshot.workstreams = [ws]
  const beads: BrBead[] = [{ id: ws.bead_id, status: 'open', labels: ['kind:objective'] }]
  const walk = (list: SnapStep[], parent: string) => list.forEach((s) => {
    beads.push({ id: s.bead_id, status: s.status, labels: ['kind:objective'], dependencies: [
      { id: parent, dependency_type: 'parent-child' },
      ...(s.blocked_by ?? []).map((id) => ({ id, dependency_type: 'blocks' })),
    ] })
    walk(s.children ?? [], s.bead_id)
  })
  walk(steps, ws.bead_id)
  return { snapshot, ws, beads }
}
const yOf = (canvas: ReturnType<typeof buildCanvas>, id: string) => [...canvas.cards, ...canvas.boxes].find((n) => n.beadId === id)!.y

// The t06.1 shape: a track whose steps S1..S4 are siblings; S1 and S2 have
// children; S3 blocks a child of S1 and S4 blocks S2 (plan order puts blocked first).
const track = () => step('t.1', [
  step('t.1.1', [step('t.1.1.1', undefined, ['t.1.3']), step('t.1.1.2')]),
  step('t.1.2', [step('t.1.2.1')], ['t.1.4']),
  step('t.1.3'),
  step('t.1.4'),
])

describe('NestedOrder', () => {
  for (const level of [1, 2, 3] as const) {
    it(`draws a sibling's blocker above the sibling holding the blocked descendant (level ${level})`, () => {
      const { snapshot, ws, beads } = build([track()])
      const canvas = buildCanvas(snapshot, wsKey(ws), 3, { level })
      expect(compareOrder(canvas, beads)).toEqual([])
      if (level === 3) {
        expect(yOf(canvas, 't.1.3')).toBeLessThan(yOf(canvas, 't.1.1.1'))
        expect(yOf(canvas, 't.1.4')).toBeLessThan(yOf(canvas, 't.1.2'))
      }
    })
  }

  it('orders three levels deep', () => {
    const deep = step('t.1', [
      step('t.1.1', [step('t.1.1.1', [step('t.1.1.1.1', undefined, ['t.1.2'])])]),
      step('t.1.2'),
    ])
    const { snapshot, ws, beads } = build([deep])
    const canvas = buildCanvas(snapshot, wsKey(ws))
    expect(compareOrder(canvas, beads)).toEqual([])
    expect(yOf(canvas, 't.1.2')).toBeLessThan(yOf(canvas, 't.1.1.1.1'))
  })

  it('keeps plan order for a cycle between containers', () => {
    const a = step('t.1.1', [step('t.1.1.1', undefined, ['t.1.2.1'])])
    const b = step('t.1.2', [step('t.1.2.1', undefined, ['t.1.1.1'])])
    const { snapshot, ws } = build([step('t.1', [a, b])])
    const canvas = buildCanvas(snapshot, wsKey(ws))
    expect(yOf(canvas, 't.1.1')).toBeLessThan(yOf(canvas, 't.1.2'))
  })

  it('ignores a blocker inside the same container', () => {
    const c = step('t.1.1', [step('t.1.1.1'), step('t.1.1.2', undefined, ['t.1.1.1'])])
    const { snapshot, ws, beads } = build([step('t.1', [c, step('t.1.2')])])
    const canvas = buildCanvas(snapshot, wsKey(ws))
    expect(compareOrder(canvas, beads)).toEqual([])
    expect(yOf(canvas, 't.1.1')).toBeLessThan(yOf(canvas, 't.1.2'))
  })

  // t06.1: a gate (G) is fed by a child of container C and blocks another child of C, so C and G
  // cannot be ordered; one such cycle must not undo the order of the chain C -> X -> Y that hangs off it.
  const t06 = () => step('t.1', [
    step('t.1.9', undefined, ['t.1.8']), // Y, planned first
    step('t.1.8', undefined, ['t.1.1']), // X waits on C
    step('t.1.1', [step('t.1.1.1'), step('t.1.1.2', undefined, ['t.1.7'])], ['t.1.7']),
    step('t.1.7', undefined, ['t.1.1.1']), // G
  ])
  for (const level of [1, 2, 3] as const) {
    it(`keeps ordering the rest around one container cycle (level ${level})`, () => {
      const { snapshot, ws, beads } = build([t06()])
      const canvas = buildCanvas(snapshot, wsKey(ws), 3, { level })
      expect(compareOrder(canvas, beads)).toEqual([])
      if (level === 3) {
        expect(yOf(canvas, 't.1.1')).toBeLessThan(yOf(canvas, 't.1.8'))
        expect(yOf(canvas, 't.1.8')).toBeLessThan(yOf(canvas, 't.1.9'))
      }
    })
  }

  it('still reports a real misorder next to the cycle', () => {
    const { snapshot, ws, beads } = build([t06()])
    const canvas = buildCanvas(snapshot, wsKey(ws))
    const x = [...canvas.cards, ...canvas.boxes].find((n) => n.beadId === 't.1.8')!
    x.y = -1 // X drawn above C, which blocks it
    expect(compareOrder(canvas, beads).map((m) => m.bead)).toContain('t.1.8')
  })
})
