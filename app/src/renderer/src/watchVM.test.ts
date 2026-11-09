import { describe, expect, it } from 'vitest'
import fixture from '../../../test/fixtures/snapshot.json'
import type { Snapshot } from '../../shared/snapshot'
import { clock, watchVM } from './viewmodel'

// The fixture's first workstream with one open step recording these waits.
function withWait(waiting: string, checked?: number): { s: Snapshot; role: string } {
  const s = structuredClone(fixture) as unknown as Snapshot
  const ws = s.workstreams[0]
  ws.steps = [{ ...ws.steps![0], status: 'in_progress', state: 'working', waiting, waiting_since: 100, waiting_checked: checked }]
  return { s, role: ws.manager_role! }
}

describe('watchVM waits', () => {
  it('labels main CI and prod tag by what they are, not as people', () => {
    const { s, role } = withWait('main CI abc1234 · prod tag abc1234')
    const vm = watchVM(s, role, {}, 200)!
    expect(vm.summary).toBe('main CI · prod tag')
    expect(vm.items.map((i) => i.kind)).toEqual(['release', 'release'])
  })

  it('names reviewers and PRs', () => {
    const { s, role } = withWait('CI #110 · review (reviewer-a, reviewer-b)')
    expect(watchVM(s, role, {}, 200)!.summary).toBe('CI #110 · review: reviewer-a, reviewer-b')
  })

  it('shows the last time cmdrd looked, from the wait', () => {
    const { s, role } = withWait('prod tag abc1234', 150)
    expect(watchVM(s, role, {}, 200)!.checkedAt).toBe(150)
  })
})

describe('time waits', () => {
  const until = 1_792_705_500
  const timed = () => {
    const { s, role } = withWait('Nightly sync')
    const step = s.workstreams[0].steps![0]
    step.waiting_until = until
    step.waiting_until_as = 'Nightly sync'
    return { s, role }
  }

  it('shows the label and the local time in the Watching row', () => {
    const { s, role } = timed()
    expect(watchVM(s, role, {}, 200)!.summary).toBe(`Nightly sync · ${clock(until)}`)
  })

})
