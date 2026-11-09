import { describe, expect, it } from 'vitest'
import { splitTickets } from '../src/features/linear/Ticket'
import type { SnapStep, SnapWorkstream } from '../src/shared/snapshot'
import { flowVM, gateName, isNow, laneParts, planOrder, segments, stageRows } from '../src/renderer/src/flow'

const step = (id: string, extra: Partial<SnapStep> = {}): SnapStep => ({ bead_id: id, title: id, state: 'not-started', status: 'open', ...extra })

describe('workstream flow', () => {
  it('orders steps after what they wait on', () => {
    const out = planOrder([step('a3', { blocked_by: ['a2'] }), step('a1'), step('a2', { blocked_by: ['a1'] })])
    expect(out.map((s) => s.bead_id)).toEqual(['a1', 'a2', 'a3'])
  })

  it('shows a gate as needing you only once its stage work is done', () => {
    const gate = { name: 'pilot', verifier: 'user', status: 'open' as const }
    const ws = {
      n: 5, title: 'Audits', project: 'p', bead_id: 'w', manager_role: 'manager-w', state: 'in-progress', status: 'open', done: 1, total: 3, project_id: 1,
      steps: [step('s1', { gates: [gate] }), step('s2', { state: 'review', gates: [gate] }), step('s3', { state: 'review', blocked_by: ['s1'], gates: [gate] }), step('s0', { state: 'done', status: 'closed' })],
    } as SnapWorkstream
    const [lane] = flowVM(ws)
    expect(lane.done).toBe(1)
    expect(lane.nodes.map((n) => [n.row.title, n.row.detail, n.gates.map((g) => [g.tone, g.detail]), n.dep])).toEqual([
      ['s1', 'Not started', [['idle', 'Your OK, once the work is done']], false],
      ['s2', 'Not started', [['needs', 'Needs your OK']], false],
      ['s3', 'After s1', [['idle', 'Your OK, once the work is done']], false],
    ])
  })

  it('a step behind open work is next, not now, even with an ask or a wait on it', () => {
    const ws = {
      n: 6, title: 'Exp', project: 'p', bead_id: 'w', manager_role: 'manager-w', state: 'in-progress', status: 'open', done: 0, total: 3, project_id: 1,
      steps: [
        step('pilot10', { working: true, owner: 'operator-x' }),
        step('ok20', { gate: true, approvers: ['user'], blocked_by: ['pilot10'] }),
        step('pilot20', { waiting: 'operator-y', blocked_by: ['ok20'] }),
      ],
    } as SnapWorkstream
    expect(flowVM(ws)[0].nodes.map((n) => [n.id, n.row.tone, isNow(n)])).toEqual([
      ['pilot10', 'working', true],
      ['ok20', 'idle', false],
      ['pilot20', 'idle', false],
    ])
  })

  it('a Manager in a turn is working on its current step only, not on every step it owns', () => {
    const ws = {
      n: 6, title: 'Exp', project: 'p', bead_id: 'w', manager_role: 'manager-w', state: 'in-progress', status: 'open', done: 0, total: 3, project_id: 1,
      steps: [
        step('find', { working: true, now: true, owner: 'manager-w' }),
        step('other', { working: true, owner: 'manager-w' }),
        step('later', { working: true, owner: 'manager-w', blocked_by: ['find'] }),
        step('op', { owner: 'operator-x', owner_state: 'working' }),
      ],
    } as SnapWorkstream
    expect(flowVM(ws)[0].nodes.map((n) => [n.id, n.row.tone])).toEqual([
      ['find', 'working'],
      ['other', 'idle'],
      ['later', 'idle'],
      ['op', 'working'],
    ])
  })

  it('marks a box that really waits on the one above it', () => {
    const ws = {
      n: 1, title: 'W', project: 'p', bead_id: 'w', manager_role: 'manager-w', state: 'in-progress', status: 'open', done: 0, total: 2, project_id: 1,
      steps: [step('b', { blocked_by: ['a'] }), step('a')],
    } as SnapWorkstream
    expect(flowVM(ws)[0].nodes.map((n) => [n.id, n.dep])).toEqual([['a', false], ['b', true]])
  })

  it('makes a lane per track when there are two or more', () => {
    const ws = {
      n: 4, title: 'Imports', project: 'p', bead_id: 'w', manager_role: 'manager-w', state: 'in-progress', status: 'open', done: 0, total: 2, project_id: 1,
      steps: [step('t1', { track: true, title: 'Track one', children: [step('t1.1', { working: true, owner: 'operator-x' })] }), step('t2', { track: true, title: 'Track two', children: [step('t2.1', { waiting: 'CI #1' })] })],
    } as SnapWorkstream
    const out = flowVM(ws)
    expect(out.map((l) => l.title)).toEqual(['Track one', 'Track two'])
    expect(out[0].nodes[0].row).toMatchObject({ tone: 'working', detail: 'Operator working' })
    expect(out[1].nodes[0].row).toMatchObject({ tone: 'waiting', detail: 'Waiting for CI #1' })
  })
})

describe('lane segments', () => {
  it('reads gate, the work between, gate: the first open gate pending on its work, later ones after', () => {
    const steps = [
      step('ok-design', { gate: true, approvers: ['user'], state: 'done', status: 'closed' }),
      step('pilot10', { working: true, owner: 'operator-x', blocked_by: ['ok-design'] }),
      step('ok20', { gate: true, approvers: ['user'], blocked_by: ['pilot10'] }),
      step('pilot20', { blocked_by: ['ok20'] }),
      step('ok100', { gate: true, approvers: ['user'], blocked_by: ['pilot20'] }),
    ]
    const titleOf = (id: string) => (id === 'ok-design' ? undefined : id)
    const out = segments(steps, titleOf)
    expect(out.map((sg) => (sg.kind === 'gate' ? `gate ${sg.gate.id} ${sg.gate.status}` : `work ${sg.nodes.map((n) => n.id).join(',')}`))).toEqual([
      'gate ok-design done',
      'work pilot10',
      'gate ok20 pending',
      'work pilot20',
      'gate ok100 later',
    ])
    const pilot20 = out[3].kind === 'work' ? out[3].nodes[0] : undefined
    expect(pilot20?.row.detail).toBe('') // right after the gate above it: no "After ok20"
  })
})

describe('lane parts', () => {
  it('folds the past, leads with the open gate and its work, shows one stretch next, folds the rest', () => {
    const steps = [
      step('a1', { state: 'done', status: 'closed' }),
      step('g1', { gate: true, approvers: ['user'], state: 'done', status: 'closed', blocked_by: ['a1'] }),
      step('b1', { state: 'done', status: 'closed', blocked_by: ['g1'] }),
      step('b2', { blocked_by: ['b1'], working: true, owner: 'operator-x' }),
      step('g2', { gate: true, approvers: ['user'], blocked_by: ['b2'] }),
      step('c1', { blocked_by: ['g2'] }),
      step('g3', { gate: true, approvers: ['user'], blocked_by: ['c1'] }),
      step('d1', { blocked_by: ['g3'] }),
      step('g4', { gate: true, approvers: ['user'], blocked_by: ['d1'] }),
    ]
    const titleOf = (id: string) => (['a1', 'g1', 'b1'].includes(id) ? undefined : id)
    const p = laneParts(segments(steps, titleOf))
    const ids = (ss: ReturnType<typeof laneParts>['current']) => ss.map((sg) => (sg.kind === 'gate' ? sg.gate.id : sg.nodes.map((n) => n.id).join(',')))
    expect([p.approved, p.doneSteps]).toEqual([1, 2])
    expect(ids(p.current)).toEqual(['b2', 'g2'])
    expect(ids(p.next)).toEqual(['c1', 'g3'])
    expect([ids(p.later), p.laterGates]).toEqual([['d1', 'g4'], 1])
  })
})

describe('stage rows', () => {
  it('reads each stage as its gate: name, condition, who has it or checks it', () => {
    const steps = [
      step('spec', { gate: true, approvers: ['user'], state: 'done', status: 'closed' }),
      step('ready', { title: 'Ready: CI green on all four PRs', state: 'done', status: 'closed', blocked_by: ['spec'], gates: [{ name: 'ready', verifier: 'auto', status: 'passed' }] }),
      step('review', { title: 'Reviews on the stack', waiting: 'review (reviewer-a)', blocked_by: ['ready'], gates: [{ name: 'pr-approved', verifier: 'reviewer:reviewer-a', status: 'open' }] }),
      step('merged', { title: 'Merge the stack in order', blocked_by: ['review'], gates: [{ name: 'merged', verifier: 'auto', status: 'open' }] }),
      step('pilot', { title: 'Move the pilot jobs', blocked_by: ['merged'], gates: [{ name: 'pilot', verifier: 'user', status: 'open' }] }),
    ]
    const titleOf = (id: string) => (['spec', 'ready'].includes(id) ? undefined : id)
    expect(stageRows(steps, titleOf).map((r) => [r.label, r.sub ?? '', r.status, r.right])).toEqual([
      ['spec', '', 'done', ''],
      ['Ready', 'Ready: CI green on all four PRs', 'done', ''],
      ['PR approved', 'Reviews on the stack', 'waiting', 'waiting for review (reviewer-a)'],
      ['Merged', 'Merge the stack in order', 'later', 'automatic'],
      ['Pilot', 'Move the pilot jobs', 'later', 'you'],
    ])
    expect(gateName('works-e2e')).toBe('Works end to end')
  })
})

describe('splitTickets', () => {
  it('leaves no stray punctuation where ticket ids were', () => {
    expect(splitTickets('Imports stuck after upstream answered (Sam: TEAM-201, TEAM-202)').text).toBe('Imports stuck after upstream answered (Sam)')
  })
})
