import { describe, expect, it } from 'vitest'
import type { ChatAccount } from './chatApi'
import { agentRows, groupRows, switchTargets, usageDetail, usageState } from './providers'

const now = 1_800_000_000

const accounts: ChatAccount[] = [
  { name: 'default', runtime: 'claude', logged_in: true, usage: { runtime: 'claude', status: 'out', out_until: now + 3600, five_hour: { utilization: 1, resets_at: now + 3600 }, role: 'x', at: '' } },
  { name: 'second', runtime: 'claude', logged_in: true, usage: { runtime: 'claude', status: 'ok', five_hour: { utilization: 0.42 }, seven_day: { utilization: 0.1 }, role: 'x', at: '' } },
  { name: 'spare', runtime: 'claude', logged_in: false },
  { name: 'codex', runtime: 'codex', logged_in: true },
]

describe('switch targets', () => {
  it('lists logged-in Claude accounts then Codex, marks the current one, and shows usage', () => {
    const t = switchTargets(accounts, { runtime: 'claude', account: 'default' }, now)
    expect(t.map((x) => [x.label, x.current, x.state, x.detail])).toEqual([
      ['Claude · default', true, 'out', expect.stringMatching(/^out until /)],
      ['Claude · second', false, 'ok', '5h 42% · 7d 10%'],
      ['Codex', false, 'unknown', 'no usage reported yet'],
    ])
    expect(t[1]).toMatchObject({ verb: 'account', body: { account: 'second' } })
    expect(t[2]).toMatchObject({ verb: 'runtime', body: { runtime: 'codex' } })
    expect(switchTargets(accounts, { runtime: 'codex' }, now).find((x) => x.current)?.label).toBe('Codex')
  })

  it('falls back to Claude and Codex when cmdrd reports no accounts', () => {
    expect(switchTargets([], undefined, now).map((x) => x.label)).toEqual(['Claude · default', 'Codex'])
  })

  it('an out account is ok again once its reset passes, and its windows start over', () => {
    const u = accounts[0].usage!
    expect(usageState(u, now)).toBe('out')
    expect(usageState(u, now + 3600)).toBe('ok')
    expect(usageDetail(u, now + 3600)).toBe('5h 0%')
  })
})

describe('agent rows', () => {
  const rows = agentRows({
    agents: { commander: { state: 'working', since: now - 120 }, 'manager-a': { state: 'idle', since: now - 7200 } },
    workstreams: [
      { title: 'A', manager_role: 'manager-a', project: 'p', bead_id: 'a' },
      { title: 'B', manager_role: 'manager-b', project: 'p', bead_id: 'b' },
    ],
    operators: [
      { role: 'operator-x', label: 'x · 1', wsTitle: 'A', wsKey: 'p:a', state: 'working' },
      { role: 'operator-done', label: 'done · 2', wsTitle: 'A', wsKey: 'p:a', state: 'idle' },
    ],
    watched: [
      { role: 'operator-x', manager: 'manager-a' },
      { role: 'operator-op-y', manager: 'manager-b' },
    ],
    chats: {
      commander: { running: true, state: 'working', runtime: 'claude' },
      'manager-a': { running: true, state: 'idle', runtime: 'claude', account: 'second' },
      'manager-b': { running: false, state: 'idle', runtime: 'claude' },
      'operator-done': { running: false, state: 'idle', runtime: 'claude' },
    },
    providers: { 'operator-x': { runtime: 'codex' } },
  })

  it('has only running agents: chats that run, Operators the watch sees', () => {
    expect(rows.map((r) => [r.role, r.provider.runtime, r.provider.account])).toEqual([
      ['commander', 'claude', 'default'],
      ['manager-a', 'claude', 'second'],
      ['operator-x', 'codex', undefined],
      ['operator-op-y', 'claude', 'default'],
    ])
  })

  it('groups by provider, account or workstream with counts', () => {
    expect(groupRows(rows, 'provider').map((g) => [g.label, g.rows.length])).toEqual([['Claude', 3], ['Codex', 1]])
    expect(groupRows(rows, 'account').map((g) => [g.label, g.rows.length])).toEqual([['Claude · default', 2], ['Claude · second', 1], ['Codex', 1]])
    expect(groupRows(rows, 'workstream').map((g) => [g.label, g.rows.length])).toEqual([['Commander', 1], ['A', 2], ['B', 1]])
  })
})

it('uses the canonical execution set including nested Managers, independent of chat poll timing', () => {
  const input = {
    executions: [
      { role: 'manager-root', state: 'idle', project_id: 1, bead_id: 'root' },
      { role: 'manager-root.1', state: 'working', project_id: 1, bead_id: 'root.1' },
      { role: 'operator-task-1', state: 'working', project_id: 1, bead_id: 'root.1' },
    ],
    agents: {}, workstreams: [{ title: 'Work', manager_role: 'manager-root', project: 'project', project_id: 1, bead_id: 'root', steps: [] }],
    operators: [], watched: [], providers: {},
    chats: { phantom: { running: true, state: 'working' } },
  }
  const rows = agentRows(input)
  expect(rows.map((r) => r.role)).toEqual(['manager-root', 'manager-root.1', 'operator-task-1'])
  expect(rows.filter((r) => r.state === 'working')).toHaveLength(2)
  expect(agentRows({ ...input, executions: [] })).toEqual([])
})
