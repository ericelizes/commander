// The workflow strip and view render cmdrd's derived workflow (display row
// `workflow`, fixture from internal/livedata/testdata/workflow/gates.json) as it is.

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { subWorkflow, workflowParts, WorkflowView } from '../src/renderer/src/components/WorkflowView'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot-workflow.json'), 'utf8')) as Snapshot
const flow = snap.display!.workstreams[0].workflow!

afterEach(cleanup)

describe('workflowParts', () => {
  it('splits around the decision: done steps, the gate, what approving starts, the rest', () => {
    const p = workflowParts(flow, 'lab-t06.6')
    expect(p.current?.title).toBe('Workflow approved: compare sample text variants')
    expect(p.done.map((s) => s.id)).toEqual(['lab-t06.1', 'lab-t06.4', 'lab-t06.5'])
    expect(p.starts.map((s) => s.id)).toEqual(['lab-t06.2.3', 'lab-t06.3.3', 'lab-t06.2.4', 'lab-t06.7'])
    expect(p.within['lab-t06.2.3']).toBe('Experiment 2: slow pages')
    expect(p.other.map((s) => s.id)).toEqual(['lab-t06.2', 'lab-t06.3', 'lab-t06.8', 'lab-t06.10'])
  })

  it('a track view is its own steps around its own current step', () => {
    const t = subWorkflow(flow, 'lab-t06.3')!
    expect(t.current).toBe('lab-t06.3.2')
    expect(t.steps.map((s) => s.id)).toEqual(['lab-t06.3.1', 'lab-t06.3.2', 'lab-t06.3.3', 'lab-t06.3.4'])
    expect(subWorkflow(flow, 'lab-t06.4')).toBeNull()
  })
})

describe('WorkflowView', () => {
  it('on a decision: done folded into a count, the gate as deciding now, then what approving starts', () => {
    render(<WorkflowView workflow={flow} focus="lab-t06.6" />)
    expect(screen.getByText('Deciding now')).toBeTruthy()
    expect(screen.getByText('Approving starts')).toBeTruthy()
    expect(screen.queryByText('Experiment 3: off-modal completed')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: /3 done/ }))
    expect(screen.getByText('Experiment 3: off-modal completed')).toBeTruthy()
    const now = document.querySelector('.wf-step.is-now')!
    expect(now.className).toContain('agent-needs')
    expect(now.textContent).toContain('Needs you')
  })

  it('renders nothing for a decision that is not in the workflow', () => {
    const { container } = render(<WorkflowView workflow={flow} focus="elsewhere-1" />)
    expect(container.innerHTML).toBe('')
  })

  it('the full view lists every open step with the words cmdrd gave it', () => {
    render(<WorkflowView workflow={flow} full />)
    expect(screen.getByText('Every step')).toBeTruthy()
    expect(screen.getAllByText('Operator working').length).toBeGreaterThan(0)
    expect(screen.getByText('Waiting for CI')).toBeTruthy()
  })
})
