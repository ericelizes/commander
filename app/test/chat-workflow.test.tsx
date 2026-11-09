// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { ChatGraph } from '../src/renderer/src/components/ChatGraph'

afterEach(cleanup)

describe('a workflow drawn in chat', () => {
  it('reads as the Graph tab stage rows: each step and gate in order, gates saying who approves', () => {
    const spec = {
      title: 'Exp 2 plan',
      nodes: [
        { id: 'a', title: 'Pick examples and a target' },
        { id: 'g1', title: 'Examples and target approved', gate: true, approvers: ['user'], blocked_by: ['a'] },
        { id: 'b', title: 'Baseline the slow endpoint', blocked_by: ['g1'] },
        { id: 'g2', title: 'Results: keep or roll back', gate: true, blocked_by: ['b'] },
      ],
    }
    const { container } = render(<ChatGraph spec={spec} />)
    expect(screen.getByText('Exp 2 plan')).toBeInTheDocument()
    const rows = [...container.querySelectorAll('.stage')].map((e) => [e.classList.contains('is-gate') ? 'gate' : 'work', e.querySelector('.stage-label')?.textContent, e.querySelector('.stage-right')?.textContent])
    expect(rows).toEqual([
      ['work', 'Pick examples and a target', ''],
      ['gate', 'CHECKPOINTExamples and target approved', 'You approve'],
      ['work', 'Baseline the slow endpoint', ''],
      ['gate', 'CHECKPOINTResults: keep or roll back', 'You approve'],
    ])
  })
})
