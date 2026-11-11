import { expect, it } from 'vitest'
import { commanderScope } from '../src/renderer/src/jurisdiction'
import fixture from './fixtures/snapshot.json'
import type { Snapshot } from '../src/shared/snapshot'

it('resolves ownership using nested beads even when ids do not share a prefix', () => {
  const ws = structuredClone((fixture as unknown as Snapshot).workstreams[0])
  ws.planning_commander = 'commander'
  const nested = structuredClone(ws.steps[0])
  nested.bead_id = 'independent-id'
  nested.planning_commander = 'commander-urgent'
  ws.steps = [nested]
  expect(commanderScope(ws, 'independent-id')).toEqual({ beadId: 'independent-id', owner: 'commander-urgent' })
  expect(commanderScope(ws)).toEqual({ beadId: ws.bead_id, owner: 'commander' })
  expect(commanderScope(ws, 'unloaded-subtree')).toBeNull()
})
