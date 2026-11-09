import { cleanup, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'

const fixture = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

afterEach(cleanup)

it('shows stage milestones even when closed recovery tasks inflate leaf counts', () => {
  const snap = structuredClone(fixture)
  const first = snap.display!.workstreams[0]
  first.progress = { done: 100, total: 101 }
  first.milestones = { done: 0, total: 3 }
  useStore.setState({ snapshot: snap, selected: { kind: 'commander' }, cursor: 'commander' })
  const { container } = render(<Navigator />)
  expect(screen.getByLabelText('0/3 stages completed')).toBeInTheDocument()
  expect(container.querySelector('.ws')!.querySelector('.ws-ring-done')).toBeNull()
})

it('labels legacy progress as tasks when the server has no milestone facts', () => {
  const snap = structuredClone(fixture)
  const first = snap.display!.workstreams[0]
  delete first.milestones
  first.progress = { done: 4, total: 7 }
  useStore.setState({ snapshot: snap, selected: { kind: 'commander' }, cursor: 'commander' })
  render(<Navigator />)
  expect(screen.getByLabelText('4/7 tasks completed')).toBeInTheDocument()
})

it('draws an empty ring for a workstream with nothing planned yet, not a full one', () => {
  const snap = structuredClone(fixture)
  const first = snap.display!.workstreams[0]
  delete first.milestones
  first.progress = { done: 0, total: 0 }
  first.text = 'Not started: Manager to plan'
  useStore.setState({ snapshot: snap, selected: { kind: 'commander' }, cursor: 'commander' })
  const { container } = render(<Navigator />)
  expect(screen.getByLabelText('0/0 tasks completed')).toBeInTheDocument()
  expect(container.querySelector('.ws')!.querySelector('.ws-ring-done')).toBeNull()
})
