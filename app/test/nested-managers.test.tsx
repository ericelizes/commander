// Nested Managers: a nested workstream with a Manager of its own (manager_role on
// its step, cmdrd with nested Managers on) gets its own chat, title and watch.
import { renderHook } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { useChatTarget } from '../src/renderer/src/components/ChatSlot'
import { managerOfRole, watchVM } from '../src/renderer/src/viewmodel'

const base = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

// nested gives the first workstream a nested workstream with its own Manager and
// one running Operator under it.
function nested(): Snapshot {
  const s = structuredClone(base)
  const ws = s.workstreams[0]
  ws.steps = [
    {
      bead_id: 'demo-a1',
      title: 'Nested area',
      state: 'in-progress',
      status: 'open',
      track: true,
      manager_role: 'manager-demo-a1',
      children: [{ bead_id: 'demo-s', title: 'Step under the nested area', state: 'in-progress', status: 'in_progress', owner: 'operator-op-x-demo-s', owner_state: 'working' }],
    } as never,
    ...(ws.steps ?? []),
  ]
  return s
}

afterEach(() => useStore.setState({ snapshot: null }))

describe('nested Managers', () => {
  it('finds the nested Manager by its role, and a top-level one as before', () => {
    const s = nested()
    const ws = s.workstreams[0]
    expect(managerOfRole(s, ws.manager_role)?.step).toBeUndefined()
    const owner = managerOfRole(s, 'manager-demo-a1')
    expect(owner?.ws.bead_id).toBe(ws.bead_id)
    expect(owner?.step?.bead_id).toBe('demo-a1')
    expect(owner?.title).toBe(`${ws.title} › Nested area`)
    expect(managerOfRole(s, 'manager-nobody')).toBeUndefined()
  })

  it('selecting the nested Manager opens its own chat', () => {
    const s = nested()
    const ws = s.workstreams[0]
    useStore.setState({ snapshot: s })
    useStore.getState().select({ kind: 'agent', key: 'manager-demo-a1', ws: `${ws.project}:${ws.bead_id}` })
    const { result } = renderHook(() => useChatTarget())
    expect(result.current).toEqual({ role: 'manager-demo-a1', title: `${ws.title} › Nested area Manager`, kind: 'manager' })
  })

  it('a nested Manager watches only its own subtree', () => {
    const s = nested()
    s.watch = undefined
    const vm = watchVM(s, 'manager-demo-a1', {}, 0)
    expect(vm).not.toBeNull()
    const text = JSON.stringify(vm)
    expect(text).toContain('demo-s')
  })
})
