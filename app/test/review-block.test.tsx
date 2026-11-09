// A plugged review block draws as one collapsed card; a click opens its checks
// with what each found. Fixture from internal/livedata review_block_test.go
// (GEN_REVIEW_FIXTURE=1), the display path cmdrd runs.

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { DerivedDiagram, flowOf, fromWorkflow } from '../src/renderer/src/components/DerivedDiagram'
import { WorkflowView } from '../src/renderer/src/components/WorkflowView'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot-review-block.json'), 'utf8')) as Snapshot
const row = snap.display!.workstreams[0]

afterEach(cleanup)

describe('review block', () => {
  it('is one step of the diagram, its checks not drawn as steps', () => {
    const flow = flowOf(row.diagram!)
    const block = flow.find((s) => s.id === 'w.9')!
    expect(block.review?.checks).toHaveLength(6)
    expect(flow.some((s) => s.id?.startsWith('w.9.'))).toBe(false)
    expect(block.now).toBe(true)
  })

  it('draws collapsed; a click opens each check with what it found', () => {
    render(<DerivedDiagram diagram={row.diagram!} />)
    expect(screen.getByText('Review gate')).toBeTruthy()
    expect(screen.queryByText('Run the code review')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show 6 checks' }))
    const list = screen.getByRole('list', { name: 'Checks' })
    expect(within(list).getByText('Run the code review')).toBeTruthy()
    expect(within(list).getByText(/Found: review-clean passed/)).toBeTruthy()
    expect(within(list).getAllByText('Operator working')).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Hide the checks' }))
    expect(screen.queryByRole('list', { name: 'Checks' })).toBeNull()
  })

  it('in the workflow view the block holds its checks instead of a nested tree', () => {
    render(<WorkflowView workflow={row.workflow!} full />)
    expect(screen.getByText(/3 of 6 checks done/)).toBeTruthy()
    expect(screen.queryByText('Final check on the current PR head')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show 6 checks' }))
    expect(screen.getByText('Final check on the current PR head')).toBeTruthy()
  })

  it('a cmdrd without the review field still draws the block as before', () => {
    const old = JSON.parse(JSON.stringify(row.workflow), (k, v) => (k === 'review' ? undefined : v))
    const d = fromWorkflow(old, row.bead_id, row.title)
    render(<DerivedDiagram diagram={d} />)
    expect(screen.getByText('Review gate')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Show \d+ checks/ })).toBeNull()
  })
})
