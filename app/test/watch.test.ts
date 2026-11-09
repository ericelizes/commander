// What a Manager between turns shows it is watching: its running Operators.
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import type { Snapshot, SnapStep } from '../src/shared/snapshot'
import { watchVM } from '../src/renderer/src/viewmodel'
import { useIntegrations } from '../src/features/linear/settings'

beforeAll(() => useIntegrations.setState({ linearTeam: 'TEAM' })) // the titles' ticket prefix
afterAll(() => useIntegrations.setState({ linearTeam: '' }))

const now = 100_000
const step = (id: string, title: string): SnapStep => ({ bead_id: id, title, state: 'in-progress', status: 'open' })
const snap = (watch: Snapshot['watch']): Snapshot =>
  ({
    version: 1,
    generated_at: '',
    projects: [],
    needs_you: [],
    agents: {},
    fleet: {},
    unread: {},
    watch,
    workstreams: [{ n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'W', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 0, total: 0, steps: Array.from({ length: 7 }, (_, i) => step(`w.${i + 1}`, `Task ${i + 1} (TEAM-1)`)) }],
  }) as unknown as Snapshot

describe('watchVM', () => {
  it('lists every Operator, quiet ones first', () => {
    const ops = Array.from({ length: 7 }, (_, i) => ({ role: `operator-${i + 1}`, bead_id: `w.${i + 1}`, active_at: now - 60 * (i + 1), since: now - 3600 }))
    ops[5].active_at = now - 25 * 60
    const vm = watchVM(snap({ 'manager-w': { checked_at: now - 12, checkin_at: now + 600, operators: ops } }), 'manager-w', {}, now)!
    expect(vm.items).toHaveLength(7)
    expect(vm.items[0]).toMatchObject({ key: 'operator-6', title: 'Task 6', slow: true, what: 'Operator quiet 25m' })
    expect(vm.items[1]).toMatchObject({ key: 'operator-1', slow: false, what: 'Operator, active 1m ago' })
    expect(vm.summary).toBe('7 Operators at work')
    expect(vm.checkedAt).toBe(now - 12)
  })
  it('watches CI and reviews it waits on, each once, CI running long first', () => {
    const s = snap({})
    const steps = s.workstreams[0].steps
    steps[0] = { ...steps[0], title: 'CI green', waiting: 'CI #4101 · CI #4107', waiting_since: now - 40 * 60 }
    steps[1] = { ...steps[1], title: 'Review comments resolved', waiting: 'review (reviewer-a, reviewer-b) · you', waiting_since: now - 600 }
    const vm = watchVM(s, 'manager-w', {}, now)!
    expect(vm.items.map((i) => [i.title, i.what, i.slow])).toEqual([
      ['CI green', 'CI on 2 PRs', true],
      ['Review comments resolved', 'review by reviewer-a, reviewer-b', false],
    ])
    expect(vm.summary).toBe('CI on 2 PRs · review: reviewer-a, reviewer-b')
  })
  it('is nothing for a Manager with nothing in motion', () => {
    expect(watchVM(snap({}), 'manager-w', {}, now)).toBeNull()
  })
  it('Commander watches every workstream and lists what has gone wrong', () => {
    const s = snap({ commander: { checked_at: now - 5, checkin_at: now + 900, operators: [], issues: [{ workstream: 'w', text: 'CI still running (CI green)', since: now - 2400 }] } })
    const vm = watchVM(s, 'commander', {}, now)!
    expect(vm.summary).toBe('1 workstream, 1 needs attention')
    expect(vm.items[0]).toMatchObject({ title: 'W', what: 'CI still running', slow: true, open: { ws: 'p:w' } })
    expect(watchVM(snap({ commander: { operators: [] } }), 'commander', {}, now)!.summary).toBe('1 workstream')
  })
})
