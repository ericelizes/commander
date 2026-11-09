// A row cmdrd corrected against what is running says why, under its state.
import { cleanup, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

afterEach(() => cleanup())

it('shows the why of a corrected row and nothing for the others', () => {
  const s = JSON.parse(JSON.stringify(snap)) as Snapshot
  const [first, ...rest] = s.display!.workstreams
  s.display = {
    ...s.display!,
    workstreams: [
      { ...first, kind: 'stalled', text: "Agent stopped on build wave 2: the Manager's move", why: 'Operator is no longer running and has not reported' },
      ...rest.map((w) => ({ ...w, why: undefined })),
    ],
  }
  useStore.setState({ snapshot: s, selected: { kind: 'commander' }, cursor: 'commander' })
  const { container } = render(<Navigator />)
  expect(screen.getByLabelText('Operator is no longer running and has not reported')).toBeInTheDocument()
  expect(container.querySelectorAll('.ws-state-why')).toHaveLength(1)
})
