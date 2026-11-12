// Tracks: separate outcomes inside one workstream, each with its own gate chain and status line.
import { describe, expect, it } from 'vitest'
import type { SnapStep, SnapWorkstream } from '../src/shared/snapshot'
import { askWhere } from '../src/features/decisions/logic'
import { openTracks } from '../src/renderer/src/viewmodel'

const s = (id: string, p: Partial<SnapStep> = {}): SnapStep => ({ bead_id: id, title: id, state: 'not-started', status: 'open', ...p })
const flow = (id: string) => s(id, { title: 'Workflow approved', gate: true, approvers: ['user'] })

// Track A waits on the user's gate; track B has an Operator on the work before its gate.
const tracked = (extra: Partial<SnapWorkstream> = {}): SnapWorkstream => ({
  n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'Audits', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 0, total: 0,
  steps: [
    s('w.1', { title: 'A. Script audits', track: true, workstream: 'w', children: [flow('w.1.1'), s('w.1.2', { blocked_by: ['w.1.1'] })] }),
    s('w.2', { title: 'B. Config audits', track: true, workstream: 'w', children: [flow('w.2.1'), s('w.2.2', { state: 'in-progress', owner: 'operator-b', blocked_by: ['w.2.1'] })] }),
  ],
  ...extra,
})


describe('tracks', () => {



  it('a parked track stays visible while a closed track is hidden', () => {
    const w = tracked()
    w.steps.push(s('w.3', { track: true, state: 'done', status: 'closed' }), s('w.4', { track: true, status: 'deferred' }))
    expect(openTracks(w).map((t) => t.bead_id)).toEqual(['w.1', 'w.2', 'w.4'])
  })




})

describe('NEEDS YOU in a track', () => {
  it('reads Workstream › Track', () => {
    expect(askWhere({ workstream: 'Audits', project: 'p', track: 'A. Script audits' })).toBe('Audits › Script audits')
    expect(askWhere({ workstream: 'Audits', project: 'p', track: 'A. Script audits', track_letter: '.2' })).toBe('Audits › Script audits')
    expect(askWhere({ workstream: 'Audits', project: 'p' })).toBe('Audits')
  })
})
