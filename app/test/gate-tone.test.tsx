// Gate colours: green approved, red blocked or failing, yellow waiting on the user,
// idle (red, dashed) not reached (an earlier stage is open) or not the user's.
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import type { SnapGate, SnapGateNode, SnapStep, SnapWorkstream } from '../src/shared/snapshot'
import { GateChecklist } from '../src/renderer/src/components/GateChecklist'
import { checkTone, stageGateTones, stageReached, stepGateTone } from '../src/renderer/src/gateTone'

afterEach(cleanup)

const gate = (name: string, verifier: string, status: SnapGate['status']): SnapGate => ({ name, verifier, status })
const s = (id: string, p: Partial<SnapStep> = {}): SnapStep => ({ bead_id: id, title: id, state: 'not-started', status: 'open', ...p })
const ws = (steps: SnapStep[], gate_nodes?: SnapGateNode[]): SnapWorkstream =>
  ({ n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'W', state: 'in-progress', status: 'open', manager_role: 'm', done: 0, total: 0, steps, gate_nodes }) as SnapWorkstream

describe('gate tones', () => {
  it('colours a check by status, verifier and whether its stage is reached', () => {
    expect(checkTone('passed', 'auto', false)).toBe('green')
    expect(checkTone('failed', 'auto', false)).toBe('red')
    expect(checkTone('open', 'user', true)).toBe('red')
    expect(checkTone('open', 'user', false)).toBe('idle')
    expect(checkTone('open', 'auto', true)).toBe('yellow')
    expect(checkTone('open', 'auto', false)).toBe('idle')
  })

  it('colours a gate bead by its state', () => {
    expect(stepGateTone({ state: 'done', status: 'closed' })).toBe('green')
    expect(stepGateTone({ state: 'blocked', status: 'open' })).toBe('red')
    expect(stepGateTone({ state: 'needs-user', status: 'open' })).toBe('red')
    expect(stepGateTone({ state: 'not-started', status: 'open', approvers: ['user'] })).toBe('idle')
    expect(stepGateTone({ state: 'in-progress', status: 'open', approvers: ['user'] })).toBe('red')
    expect(stepGateTone({ state: 'in-progress', status: 'open', approvers: ['reviewer-a'] })).toBe('yellow')
  })

  const w = ws([
    s('spec', { state: 'in-progress', gates: [gate('Spec ok', 'user', 'open'), gate('Tests', 'auto', 'failed')] }),
    s('build', { blocked_by: ['spec'], gates: [gate('Sign off', 'user', 'open')] }),
  ])

  it('derives reached from earlier open stages', () => {
    expect(stageReached(w, 'spec')).toBe(true)
    expect(stageReached(w, 'build')).toBe(false)
    expect(stageGateTones(w, 'spec', w.steps[0].gates!)).toEqual(['red', 'red'])
    expect(stageGateTones(w, 'build', w.steps[1].gates!)).toEqual(['idle'])
  })

  it('prefers gate_nodes and falls back to gates[] when there are none', () => {
    const node: SnapGateNode = { kind: 'gate', bead_id: 'g1', stage: 'spec', name: 'Spec ok', verifier: 'user', status: 'passed', blocked_by: ['spec'], blocks: [] }
    expect(stageGateTones(ws(w.steps, [node]), 'spec', w.steps[0].gates!)).toEqual(['green', 'red'])
    expect(stageGateTones(ws(w.steps, []), 'spec', w.steps[0].gates!)).toEqual(['red', 'red'])
  })

  it('shows the tone on the checklist', () => {
    const { container } = render(<GateChecklist gates={w.steps[0].gates!} tones={['yellow', 'red']} />)
    expect(container.querySelectorAll('.tone-yellow')).toHaveLength(1)
    expect(container.querySelectorAll('.tone-red')).toHaveLength(1)
  })
})
