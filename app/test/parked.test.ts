// A bead parked with `br defer` is out of play: not the next gate, not progress, not working.
import { describe, expect, it } from 'vitest'
import type { Snapshot, SnapStep, SnapWorkstream } from '../src/shared/snapshot'
import { deriveSnapshot, gateStatus, progress, wsKey } from '../src/renderer/src/viewmodel'
import { buildCanvas } from '../src/renderer/src/graph'

const s = (id: string, p: Partial<SnapStep> = {}): SnapStep => ({ bead_id: id, title: id, state: 'not-started', status: 'open', ...p })
const parked = (id: string, p: Partial<SnapStep> = {}) => s(id, { status: 'deferred', ...p })
const ws = (steps: SnapStep[]): SnapWorkstream => ({ n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'W', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 0, total: 0, steps })

// The t02 shape: track A is parked (older gate), track B is live.
const t02 = () =>
  ws([
    parked('a', {
      title: 'A. Script audits',
      children: [
        parked('a-flow', { title: 'Workflow approved (A)', gate: true, approvers: ['user'] }),
        parked('a1', { blocked_by: ['a-flow'], owner: 'operator-a1', state: 'in-progress', waiting: 'you' }),
      ],
    }),
    s('b', { title: 'B', children: [s('b-flow', { title: 'Workflow approved (B)', gate: true, approvers: ['user'] }), s('b1', { state: 'in-progress', owner: 'operator-b1', blocked_by: ['b-flow'] })] }),
  ])

describe('parked (deferred) beads', () => {
  it('progress ignores parked work', () => {
    const w = deriveSnapshot({ workstreams: [t02()], agents: {} } as unknown as Snapshot).workstreams[0]
    expect(progress(w)).toEqual({ done: 0, total: 1 })
  })
  it('the next gate skips a parked gate even when it is the oldest', () => {
    expect(gateStatus(t02())?.gate.bead_id).toBe('b-flow')
  })
  it('a workstream with only parked gates has no next gate', () => {
    expect(gateStatus(ws([parked('g', { gate: true, approvers: ['user'] })]))).toBeNull()
  })
  it('a parent shows nothing moving when its only children are parked', () => {
    const w = deriveSnapshot({ workstreams: [ws([s('x', { children: [parked('y', { state: 'in-progress' })] })])], agents: {} } as unknown as Snapshot).workstreams[0]
    expect(w.steps[0].state).toBe('not-started')
  })
  it('the graph draws a parked gate as Parked, status faithful, never red', () => {
    const snap = deriveSnapshot({ workstreams: [t02()], agents: {} } as unknown as Snapshot)
    const canvas = buildCanvas(snap, wsKey(snap.workstreams[0]))
    const card = canvas.cards.find((c) => c.beadId === 'a-flow')
    expect(card?.status).toBe('deferred')
    expect(card?.tone).toBe('idle')
  })
})
