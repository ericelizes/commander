// A workstream's status is its next approval gate and how close it is.
import { describe, expect, it } from 'vitest'
import type { Snapshot, SnapStep, SnapWorkstream } from '../src/shared/snapshot'
import { deriveSnapshot, gateStatus } from '../src/renderer/src/viewmodel'

const s = (id: string, p: Partial<SnapStep> = {}): SnapStep => ({ bead_id: id, title: id, state: 'not-started', status: 'open', ...p })
const done = (id: string, p: Partial<SnapStep> = {}) => s(id, { state: 'done', status: 'closed', ...p })
const ws = (steps: SnapStep[]): SnapWorkstream => ({ n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'W', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 0, total: 0, steps })

// Background jobs: spike → [Spec approved: user] → build (3 units) → [PRs approved: reviewer-a, reviewer-b] → merge
const jobs = (buildDone: number, op?: string) =>
  ws([
    done('spike'),
    done('spec-ok', { title: 'Spec approved', gate: true, approvers: ['user'], blocked_by: ['spike'] }),
    s('build', {
      blocked_by: ['spec-ok'],
      ...(buildDone === 3 ? { state: 'done', status: 'closed' } : {}),
      children: [0, 1, 2].map((i) => (i < buildDone ? done(`u${i}`) : s(`u${i}`, { state: i === buildDone && op ? 'in-progress' : 'not-started', owner: i === buildDone ? op : undefined }))),
    }),
    s('prs-ok', { title: 'PR approval', gate: true, approvers: ['reviewer-a', 'reviewer-b'], blocked_by: ['build'] }),
    s('merge', { blocked_by: ['prs-ok'] }),
  ])

describe('gates', () => {
  it('finds the next gate and counts only the work since the last approved one', () => {
    expect(gateStatus(jobs(1))).toMatchObject({ gate: { bead_id: 'prs-ok' }, at: false, done: 1, total: 3 })
  })

  it('a lone workflow gate is still the next gate', () => {
    const w = ws([s('flow', { title: 'Workflow approved', gate: true, approvers: ['user'] })])
    expect(gateStatus(w)?.gate.bead_id).toBe('flow')
  })
})

// example-app-t04.3: an open stage whose only sub-task is closed is not done.
describe('a stage is its own status, sub-tasks are progress', () => {
  const stage = (p: Partial<SnapStep> = {}) => s('deployed', { blocked_by: ['wf'], children: [done('deployed.1')], ...p })
  const w = () => ws([done('wf', { gate: true, approvers: ['user'] }), stage(), s('greenlight', { title: 'Greenlight', gate: true, approvers: ['user'], blocked_by: ['deployed'] })])
  const snap = (steps: SnapStep[]) => ({ workstreams: [ws(steps)], agents: {} }) as unknown as Snapshot

  it('is not done and counts as one open unit before the next gate', () => {
    const d = deriveSnapshot(snap(w().steps)).workstreams[0]
    expect(d.steps[1].state).toBe('in-progress')
    expect(gateStatus(d)).toMatchObject({ gate: { bead_id: 'greenlight' }, at: false, done: 0, total: 1 })
  })
  it('an older cmdrd that rolled it up to done is corrected client-side', () => {
    const d = deriveSnapshot(snap(w().steps.map((x) => (x.bead_id === 'deployed' ? stage({ state: 'done' }) : x)))).workstreams[0]
    expect(d.steps[1].state).toBe('in-progress')
  })
  it('is done once its own bead is closed', () => {
    const d = deriveSnapshot(snap(w().steps.map((x) => (x.bead_id === 'deployed' ? done('deployed', { children: [done('deployed.1')] }) : x)))).workstreams[0]
    expect(gateStatus(d)).toMatchObject({ at: true, done: 1, total: 1 })
  })
})
