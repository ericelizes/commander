import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import { useStore } from '../store'
import { HealthBadge } from './HealthBadge'

afterEach(cleanup)

describe('HealthBadge', () => {
  it('is a count that opens the list of what is not working', async () => {
    useStore.setState({ snapshot: { workstreams: [], needs_you: [], health: [{ key: 'mail:manager-x', detail: "Mail can't reach manager-x\nmore", since: 1793300000 }] } as never })
    render(<HealthBadge />)
    await userEvent.click(screen.getByRole('button', { name: /1 not working/ }))
    expect(screen.getByRole('dialog')).toHaveTextContent("Mail can't reach manager-x")
    expect(screen.getByRole('dialog')).not.toHaveTextContent('more')
  })

  it('is not there when everything works', () => {
    useStore.setState({ snapshot: { workstreams: [], needs_you: [] } as never })
    render(<HealthBadge />)
    expect(screen.queryByRole('button')).toBeNull()
  })
})

it('counts graph findings individually, excludes duplicate health groups and tracks, and separates blockers', async () => {
  const findings = [
    { check: 'unheld', bead: 'w.1', why: 'work has no holder' },
    { check: 'needs-you-lane', bead: 'w.2', why: 'card drawn elsewhere' },
    { check: 'MERGE_REVIEW_INCOMPLETE', bead: 'w.3', why: 'review pending' },
    { check: 'MERGE_REVIEW_REQUIRED', bead: 'w.4', why: 'plug review' },
    { check: 'MERGE_REVIEW_REQUIRED', bead: 'w.5', why: 'no PR yet', advisory: true },
  ]
  useStore.setState({ snapshot: {
    workstreams: [], needs_you: [],
    health: [{ key: 'plan:manager-w', detail: 'grouped plan errors', since: 1 }],
    display: { workstreams: [{ key: 'p:w', diagram: { findings, tracks: [{ findings }] } }] },
  } as never })
  render(<HealthBadge />)
  const badge = screen.getByRole('button', { name: '2 errors, 2 blockers, 1 advisory' })
  expect(badge).not.toHaveClass('only-wrong')
  await userEvent.click(badge)
  expect(screen.getByRole('dialog')).toHaveTextContent('Workflow errors (2)')
  expect(screen.getByRole('dialog')).toHaveTextContent('Blockers (2)')
  expect(screen.getByRole('dialog')).not.toHaveTextContent('grouped plan errors')
})

it('matches the graph/check-in scenario: 11 health groups become 8 errors, 5 blockers and 2 advisories', async () => {
  const { default: scenario } = await import('../../../../../tests/evals/scenarios/truthful-checkin.json')
  useStore.setState({ snapshot: scenario.snapshot as never })
  render(<HealthBadge />)
  expect(screen.getByRole('button', { name: '8 errors, 5 blockers, 2 advisories' })).toBeInTheDocument()
})

it('stays amber with only expected blockers and advisories', () => {
  const findings = [
    { check: 'MERGE_REVIEW_INCOMPLETE', bead: 'w.3', why: 'review pending' },
    { check: 'MERGE_REVIEW_REQUIRED', bead: 'w.5', why: 'no PR yet', advisory: true },
  ]
  useStore.setState({ snapshot: { workstreams: [], needs_you: [], display: { workstreams: [{ key: 'p:w', diagram: { findings } }] } } as never })
  render(<HealthBadge />)
  expect(screen.getByRole('button', { name: '1 blocker, 1 advisory' })).toHaveClass('only-wrong')
})
