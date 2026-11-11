import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { SnapStep, SnapWorkstream, Snapshot } from '../src/shared/snapshot'
import {
  roleLabel,
  activateWorkstream,
  deriveSnapshot,
  deriveStep,
  fmtAge,
  plainTitle,
  fmtUSD,
  hiddenLabels,
  navOrder,
  finishedOperatorsVM,
  fleetVM,
  roleCostNote,
  board,
  operatorsVM,
  shortcut,
  sparkPoints,
  stepRows,
  type NavState,
} from '../src/renderer/src/viewmodel'
import { apiConfig, commanderHome } from '../src/main/config'
import { loopbackOrigin } from '../src/renderer/src/host'

const fixture = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const now = 1792536049 + 14 * 60

const step = (id: string, state: string, extra: Partial<SnapStep> = {}): SnapStep => ({
  bead_id: id,
  title: id,
  state,
  status: state === 'done' ? 'closed' : 'open',
  ...extra,
})

const ws = (steps: SnapStep[], extra: Partial<SnapWorkstream> = {}): SnapWorkstream => ({
  n: 1,
  project: 'p',
  project_id: 1,
  bead_id: 'p-1',
  title: 'WS',
  state: 'not-started',
  status: 'open',
  manager_role: 'manager-p-1',
  done: 0,
  total: steps.length,
  steps,
  ...extra,
})

describe('agentLine', () => {

  it('formats ages compactly', () => {
    expect([fmtAge(5), fmtAge(61), fmtAge(3 * 3600 + 5), fmtAge(2 * 86400)]).toEqual(['5s', '1m', '3h', '2d'])
  })
})


describe('stepRows', () => {
  it('expands the phase holding the now-step and collapses done and overflow', () => {
    const steps = [
      step('p-1.1', 'done'),
      step('p-1.2', 'in-progress', {
        title: 'Phase B',
        children: [step('p-1.2.1', 'done'), step('p-1.2.2', 'in-progress', { title: 'U2 build' }), step('p-1.2.3', 'not-started', { title: 'U3 ship' })],
      }),
      step('p-1.3', 'not-started', { title: 'Phase C', phase_note: 'not started', children: [step('p-1.3.1', 'not-started')] }),
    ]
    const rows = stepRows(ws(steps, { now_step: 'p-1.2.2' }), true)
    expect(rows.map((r) => `${r.depth}:${r.marker ?? r.kind}:${r.text}${r.note ? ` · ${r.note}` : ''}`)).toEqual([
      '0:run:Phase B',
      '1:spin:U2 build',
      '1:open:U3 ship',
      '1:done:✓ 1 done',
      '0:open:Phase C · not started',
      '0:done:✓ 1 done',
    ])
    expect(rows.filter((r) => r.now).map((r) => r.text)).toEqual(['U2 build'])
  })

  it('spins only while the Manager works, marks reviews, asks verb first', () => {
    const steps = [step('a', 'in-progress', { review: '4107' }), step('b', 'needs-user', { ask: 'Approve the PR' }), step('c', 'in-progress')]
    const idle = stepRows(ws(steps, { now_step: 'c' }), false)
    expect(idle.map((r) => r.marker)).toEqual(['review', 'needs', 'run'])
    expect(idle[0].pr).toBe('#4107')
    expect(idle[1].text).toBe('Approve the PR')
    expect(stepRows(ws(steps, { now_step: 'c' }), true)[2].marker).toBe('spin')
  })

  it('hides open steps past six behind "+N more" with code ranges', () => {
    const steps = Array.from({ length: 9 }, (_, i) => step(`s${i + 1}`, 'not-started', { title: `U${i + 1} unit` }))
    const rows = stepRows(ws(steps), false)
    expect(rows).toHaveLength(7)
    expect(rows[6]).toMatchObject({ kind: 'more', text: '+3 more · U7–U9' })
  })

  it('names owners other than the Manager', () => {
    const rows = stepRows(ws([step('a', 'not-started', { owner: 'manager-p-1' }), step('b', 'not-started', { owner: 'user' })]), false)
    expect(rows.map((r) => r.owner)).toEqual([undefined, 'user'])
  })

  it('labels hidden steps by code ranges, else short titles', () => {
    expect(hiddenLabels([step('a', 'x', { title: 'U2 a' }), step('b', 'x', { title: 'U4 b' }), step('c', 'x', { title: 'Something long enough' })])).toBe(
      'U2, U4, Something lon…',
    )
  })
})

describe('the real-API fixture', () => {
  const vm = board(fixture)

  it('renders the fleet card and orders NEEDS YOU, Commander, then the workstreams', () => {
    expect(fleetVM(fixture.fleet)).toMatchObject({ today: '$0', agents: fixture.fleet.agents })
    expect(navOrder(vm)[0]).toBe(vm.asks[0].key) // NEEDS YOU on top
    expect(navOrder(vm)[vm.asks.length]).toBe('commander')
    expect(navOrder(vm)).toHaveLength(1 + vm.asks.length + vm.workstreams.length)
  })

  it('every workstream renders a steps tree with exactly one bold now-step at most', () => {
    for (const w of fixture.workstreams) {
      const rows = stepRows(w, true)
      expect(rows.filter((r) => r.now).length).toBeLessThanOrEqual(1)
      expect(rows.filter((r) => r.marker === 'spin').length).toBeLessThanOrEqual(1)
    }
  })
})

describe('go', () => {
  const vm = board(fixture)
  const start: NavState = { selected: { kind: 'commander' } }

  it('one activation selects the workstream', () => {
    expect(activateWorkstream(start, 'x').selected).toEqual({ kind: 'ws', key: 'x' })
  })

  it('Ctrl+0 is Commander, Ctrl+n the nth workstream, out of range is a no-op', () => {
    expect(shortcut(start, vm, 1).selected).toEqual({ kind: 'ws', key: vm.workstreams[0].key })
    expect(shortcut(shortcut(start, vm, 1), vm, 0).selected).toEqual({ kind: 'commander' })
    expect(shortcut(start, vm, 9)).toBe(start)
  })
})

describe('misc', () => {
  it('formats cost and the sparkline', () => {
    expect([fmtUSD(0), fmtUSD(3.456), fmtUSD(42.2), fmtUSD(1520)]).toEqual(['$0', '$3.46', '$42', '$1.5k'])
    expect(sparkPoints([0, 0], 10, 10)).toBe('0.0,9.0 10.0,9.0')
    expect(sparkPoints([0, 2], 10, 10)).toBe('0.0,9.0 10.0,1.0')
  })

  it('reads COMMANDER_HOME for the API url and token', () => {
    const home = join(__dirname, 'fixtures', 'home')
    expect(commanderHome({ COMMANDER_HOME: home })).toBe(home)
    expect(apiConfig({ COMMANDER_HOME: home })).toEqual({ apiUrl: 'http://127.0.0.1:8799', token: 'test-token' })
    expect(apiConfig({ COMMANDER_HOME: home, COMMANDER_API_URL: 'http://127.0.0.1:1/' }).apiUrl).toBe('http://127.0.0.1:1')
  })

  it('a token link may name only a loopback API', () => {
    expect(loopbackOrigin('http://127.0.0.1:8787/x')).toBe('http://127.0.0.1:8787')
    expect(loopbackOrigin('http://localhost:9000')).toBe('http://localhost:9000')
    expect(loopbackOrigin('https://evil.example')).toBeNull()
    expect(loopbackOrigin('javascript:alert(1)')).toBeNull()
    expect(loopbackOrigin(null)).toBeNull()
  })
})

describe('operatorsVM (YOUR AGENTS: Operators tier)', () => {
  it('lists every Operator across workstreams with task, branch, state and cost', () => {
    const OP = 'operator-op-exp-3-sample-example-app-t06-1-1'
    const ops = operatorsVM(fixture, {
      [OP]: { running: true, state: 'working', total_cost_usd: 0.41, cwd: '/w/worktrees/op-exp-3-sample/' },
    })
    expect(ops.map((o) => o.label)).toEqual(['exp-3-sample · 1-1', 'fix-assistant-sh · 6-1']) // the done step's owner is gone
    expect(ops[0]).toMatchObject({ native: true, state: 'working', cost: 0.41, branch: 'op-exp-3-sample', wsKey: 'example-app:example-app-t06' })
    expect(ops[0].task).toMatch(/^Exp 3: does the sample prompt/)
    expect(ops[1]).toMatchObject({ native: false, state: 'tmux', cost: undefined, branch: undefined })
  })
})

describe('finishedOperatorsVM (recently finished Operators stay visible)', () => {
  const base = fixture.workstreams[0]
  const withFinished = (finished: NonNullable<Snapshot['watch']>[string]['finished']): Snapshot => ({
    ...fixture,
    watch: { [base.manager_role ?? '']: { operators: [], finished } },
  })
  it('shows finished/died for 10 minutes, newest first, with the report line', () => {
    const snap = withFinished([
      { role: 'operator-op-a-1', bead_id: 'x-1', at: now - 120, report: 'Fixed it; tests green' },
      { role: 'operator-op-b-2', bead_id: 'x-2', at: now - 30, died: true, report: 'exited without a final report' },
      { role: 'operator-op-c-3', bead_id: 'x-3', at: now - 11 * 60 },
    ])
    const vm = finishedOperatorsVM(snap, now)
    expect(vm.map((f) => [f.role, f.state, f.report])).toEqual([
      ['operator-op-b-2', 'died just now', 'exited without a final report'],
      ['operator-op-a-1', 'finished 2m ago', 'Fixed it; tests green'],
    ])
    expect(vm[0].died).toBe(true)
  })
  it('is empty when nothing finished', () => {
    expect(finishedOperatorsVM(fixture, now)).toEqual([])
  })
})

describe('deriveStep', () => {
  const agents = { 'operator-a': { state: 'working', since: 1 }, 'operator-b': { state: 'idle', since: 1 } }
  const tree = (...kids: SnapStep[]) => step('p', 'not-started', { children: kids })

  it('a working agent deep below makes every ancestor in progress and working', () => {
    const p = deriveStep(tree(step('c', 'not-started', { children: [step('u', 'not-started', { owner: 'operator-a' })] })), agents)
    expect([p.state, p.working]).toEqual(['in-progress', true])
    expect(p.children![0].state).toBe('in-progress')
  })
  it('an idle owner is in progress but not working', () => {
    const p = deriveStep(tree(step('u', 'not-started', { owner: 'operator-b' })), agents)
    expect([p.state, p.working]).toEqual(['in-progress', false])
  })
  it('review below reads in progress; all done reads done; nothing reads not started', () => {
    expect(deriveStep(tree(step('u', 'review')), agents).state).toBe('in-progress')
    // the parent is done only when its own bead is closed; children done alone is progress
    expect(deriveStep(tree(step('u', 'done'), step('v', 'done')), agents).state).toBe('in-progress')
    expect(deriveStep({ ...tree(step('u', 'done')), status: 'closed' }, agents).state).toBe('done')
    expect(deriveStep(tree(step('u', 'not-started')), agents).state).toBe('not-started')
  })
  it('needs-user below wins; blocked on the row itself wins', () => {
    expect(deriveStep(tree(step('u', 'in-progress'), step('v', 'needs-user')), agents).state).toBe('needs-user')
    expect(deriveStep(step('p', 'blocked', { children: [step('u', 'in-progress')] }), agents).state).toBe('blocked')
  })
  it('deriveSnapshot lifts a stale workstream row from the old snapshot', () => {
    const ws = { ...(fixture.workstreams[0] as SnapWorkstream), state: 'not-started', steps: [tree(step('u', 'not-started', { owner: 'operator-a' }))] }
    const out = deriveSnapshot({ ...fixture, agents, workstreams: [ws] })
    expect(out.workstreams[0].state).toBe('in-progress')
    expect(out.workstreams[0].working).toBe(true)
    expect(out.workstreams[0].steps[0].state).toBe('in-progress')
  })
})

describe('PostgreSQL observed execution spinners', () => {
  const tree = () => ws([step('nested', 'in-progress', { working: true, owner: 'manager-p-1', children: [step('leaf', 'in-progress', { working: true })] })], { working: true })
  const base = () => ({ ...fixture, storage: 'postgres', agents: { 'manager-p-1': { state: 'working', since: 1 } }, workstreams: [tree()] })
  it('clears stale nested spinners when there is no verified execution, even with a working planning chat', () => {
    const out = deriveSnapshot({ ...base(), executions: [] })
    expect(out.workstreams[0].working).toBe(false)
    expect(out.workstreams[0].steps[0].working).toBe(false)
    expect(out.workstreams[0].steps[0].children![0].working).toBe(false)
  })
  it('propagates only a verified active run through its ancestors', () => {
    const run = { role: 'worker', state: 'working', project_id: 1, bead_id: 'leaf', run_id: 17, session_id: 'exact-session' }
    const out = deriveSnapshot({ ...base(), executions: [run] })
    expect(out.workstreams[0].working).toBe(true)
    expect(out.workstreams[0].steps[0].working).toBe(true)
    expect(out.workstreams[0].steps[0].children![0].working).toBe(true)
    for (const candidate of [{ ...run, state: 'unknown' }, { ...run, run_id: 0 }, { ...run, project_id: 2 }]) {
      expect(deriveSnapshot({ ...base(), executions: [candidate] }).workstreams[0].working).toBe(false)
    }
  })
})

describe('chat role spend', () => {
  const fleet = {
    ...fixture.fleet,
    cost_usd_24h: 12,
    chat_cost_usd_24h: 5,
    roles_cost: [
      { role: 'commander', today_usd: 2, session_usd: 2, total_usd: 20, input_tokens: 1, output_tokens: 1 },
      { role: 'manager-a', today_usd: 2, session_usd: 1, total_usd: 7.5, input_tokens: 1, output_tokens: 1 },
      { role: 'manager-b', today_usd: 1, session_usd: 1, total_usd: 1, input_tokens: 1, output_tokens: 1 },
    ],
  }
  it('breaks the 24h figure into Operators, Commander and Managers', () => {
    expect(fleetVM(fleet).breakdown).toBe('Last 24h, estimated (plan-equivalent): Operators $7.00 · Commander $2.00 · Managers $3.00')
  })
  it('notes a role\'s spend for the chat header, nothing when unrecorded', () => {
    expect(roleCostNote(fleet, 'manager-a')).toBe('est. $2.00 today, $7.50 total')
    expect(roleCostNote(fleet, 'manager-zzz')).toBeUndefined()
    expect(roleCostNote(undefined, 'commander')).toBeUndefined()
  })
})

describe('roleLabel', () => {
  const snap = {
    workstreams: [
      { n: 5, title: 'Audits', project: 'example-app', bead_id: 'example-app-t02', manager_role: 'manager-example-app-t02', steps: [{ bead_id: 'example-app-t02.7', title: 'Log audits', track: true, state: 'open', status: 'open' }] },
      { n: 1, title: 'Critical security (TEAM-101)', project: 'example-app', bead_id: 'example-app-t04', manager_role: 'manager-example-app-t04', steps: [] },
    ],
  } as unknown as Snapshot

  it('names Commander, a Manager by its workstream, an agent in a track by both', () => {
    expect(roleLabel(snap, 'commander')).toBe('Commander')
    expect(roleLabel(snap, 'manager-example-app-t04')).toBe('1 Critical security')
    expect(roleLabel(snap, 'operator-op-fix-example-app-t02.7.3')).toBe('5 Audits › Log audits')
    expect(roleLabel(snap, 'operator-op-fix-example-app-t02-7-3')).toBe('5 Audits › Log audits')
    expect(roleLabel(snap, 'operator-op-x-example-app-t02.2')).toBe('5 Audits')
    expect(roleLabel(snap, 'BlueLake')).toBe('BlueLake')
  })
})

describe('plainTitle', () => {
  it('drops only the configured ticket prefix', () => {
    expect(plainTitle('Spike the spec (TEAM-101)', 'team')).toBe('Spike the spec')
    expect(plainTitle('Ship (TEAM-1) now', 'TEAM')).toBe('Ship now')
    expect(plainTitle('Decode input as (UTF-8)', 'TEAM')).toBe('Decode input as (UTF-8)')
    expect(plainTitle('Hash files with (SHA-256)', 'TEAM')).toBe('Hash files with (SHA-256)')
    expect(plainTitle('Dates in (ISO-8601)', 'TEAM')).toBe('Dates in (ISO-8601)')
    expect(plainTitle('Other team (OPS-12)', 'TEAM')).toBe('Other team (OPS-12)')
    expect(plainTitle('Too long (TEAM-1234567)', 'TEAM')).toBe('Too long (TEAM-1234567)')
    expect(plainTitle('Spike the spec (TEAM-101)', '')).toBe('Spike the spec (TEAM-101)')
    expect(plainTitle('Spike the spec (TEAM-101)', 'not a key')).toBe('Spike the spec (TEAM-101)')
  })
})
