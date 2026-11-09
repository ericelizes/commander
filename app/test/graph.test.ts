// The task canvas is the beads, raw: a box is a bead with children, an arrow a real dependency.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { buildCanvas } from '../src/renderer/src/graph'
import { wsKey } from '../src/renderer/src/viewmodel'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const inside = (r: { x: number; y: number }, b: { x: number; y: number; w: number; h: number }) => r.x >= b.x && r.y >= b.y && r.x <= b.x + b.w && r.y <= b.y + b.h

describe('buildCanvas', () => {
  it('holds a workstream\'s steps in its box and draws no arrows without real dependencies', () => {
    const ws = snap.workstreams[0]
    const c = buildCanvas(snap, wsKey(ws))
    const box = c.boxes.find((b) => b.kind === 'workstream')!
    expect(box.agent).toBe(ws.manager_role)
    for (const card of c.cards) expect(inside(card, box)).toBe(true)
    expect(c.edges).toEqual([])
  })

  it('shows a real dependency (an arrow from the bead right above, else a marker), and stacks the waited-on step first', () => {
    const s2 = JSON.parse(JSON.stringify(snap)) as Snapshot
    const ws = s2.workstreams.find((w) => w.steps.filter((x) => x.state !== 'done' && !x.children?.length).length >= 2)!
    const [first, second] = ws.steps.filter((x) => x.state !== 'done' && !x.children?.length)
    first.blocked_by = [second.bead_id] // the later step in plan order is the one waited on
    const c = buildCanvas(s2, wsKey(ws))
    const from = `${wsKey(ws)}:${second.bead_id}`
    const to = `${wsKey(ws)}:${first.bead_id}`
    const waiter = c.cards.find((x) => x.id === to)!
    const arrow = c.edges.some((e) => e.from === from && e.to === to)
    expect(arrow || waiter.markers?.some((m) => m.id === second.bead_id)).toBe(true)
    expect(c.cards.find((x) => x.id === from)!.y).toBeLessThan(c.cards.find((x) => x.id === to)!.y)
  })

  it('puts a step\'s sub-steps inside its box with no arrows between them', () => {
    const ws = snap.workstreams.find((w) => w.steps.some((s) => s.children?.some((c) => c.state !== 'done')))
    if (!ws) return
    const c = buildCanvas(snap, wsKey(ws))
    const parent = c.boxes.find((b) => b.kind === 'bead')!
    const parts = c.cards.filter((card) => inside(card, parent))
    expect(parts.length).toBeGreaterThan(0)
    const ids = new Set(parts.map((p) => p.id))
    expect(c.edges.some((e) => ids.has(e.from) && ids.has(e.to))).toBe(false)
  })

  it('shows every bead, finished ones included, and the workstream as a bead', () => {
    const ws = snap.workstreams[0]
    const c = buildCanvas(snap, wsKey(ws))
    const count = (steps: typeof ws.steps): number => steps.reduce((n, s) => n + 1 + count(s.children ?? []), 0)
    expect(c.cards.length + c.boxes.length).toBe(1 + count(ws.steps))
    expect(c.boxes.find((b) => b.kind === 'workstream')!.beadId).toBe(ws.bead_id)
  })

  it('lays the fleet out as a grid of workstream boxes with no cards overlapping', () => {
    const c = buildCanvas(snap, undefined, 2)
    expect(c.boxes.filter((b) => b.kind === 'workstream')).toHaveLength(snap.workstreams.length)
    const overlap = c.cards.some((a, i) => c.cards.some((b, j) => i < j && a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h))
    expect(overlap).toBe(false)
  })
})
