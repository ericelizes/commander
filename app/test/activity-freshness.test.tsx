import { act, cleanup, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { Overview } from '../src/renderer/src/components/Overview'
import { activityUnverified } from '../src/renderer/src/components/ActivityFreshness'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const scenario = JSON.parse(readFileSync(join(__dirname, '../../tests/evals/scenarios/activity-freshness.json'), 'utf8')) as { cases: { name: string; age: number; error: boolean; warning: boolean }[] }
afterEach(cleanup)
it('evaluates connected stalled, unchanged healthy, failed load and recovery in both views', () => {
  const now = Math.floor(Date.now() / 1000)
  const s = { ...snap, generated_at: new Date(0).toISOString() }
  useStore.setState({ snapshot: s, conn: 'live', nowSec: now, config: null })
  const row = s.display!.workstreams[0]
  const ws = s.workstreams.find((w) => w.bead_id === row.bead_id) ?? s.workstreams[0]
  render(<><Navigator /><Overview ws={ws} row={row} onWorkflow={() => {}} onTrack={() => {}} /></>)
  for (const c of scenario.cases) {
    // Same version/generated_at and recorded decisions throughout the sequence.
    act(() => useStore.setState({ snapshot: { ...s, load_completed_at: new Date((now - c.age) * 1000).toISOString(), load_error: c.error } }))
    expect(screen.queryAllByText(/Current activity unverified/), c.name).toHaveLength(c.warning ? 2 : 0)
    expect(document.querySelector(`[data-nav-key="${row.key}"]`), c.name).toBeInTheDocument()
    expect(activityUnverified(useStore.getState().snapshot, 'live', now), c.name).toBe(c.warning)
  }
  act(() => useStore.setState({ nowSec: now + 60 }))
  expect(screen.queryAllByText(/Current activity unverified/)).toHaveLength(2)
  act(() => useStore.setState({ nowSec: now }))
  expect(screen.queryByText(/Current activity unverified/)).not.toBeInTheDocument()
  expect(useStore.getState().snapshot?.needs_you).toEqual(s.needs_you)
})
it('does not use generated_at or connection as successful read evidence', () => {
  const now = Date.now() / 1000
  expect(activityUnverified({ ...snap, load_completed_at: undefined }, 'live', now)).toBe(true)
  expect(activityUnverified({ ...snap, load_completed_at: new Date().toISOString() }, 'offline', now)).toBe(true)
})
