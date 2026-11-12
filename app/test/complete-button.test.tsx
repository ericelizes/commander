// A finished workstream (kind done) shows Complete in its state line; the click closes it through
// /api/cmd and the row reads Completed before the board catches up.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const closeAll = (steps: Snapshot['workstreams'][number]['steps'] = []): typeof steps =>
  steps.map((s) => ({ ...s, state: 'done', status: 'closed', owner: undefined, waiting: undefined, working: undefined, children: closeAll(s.children ?? []) }))

let bodies: unknown[]

beforeEach(() => {
  bodies = []
  const done = JSON.parse(JSON.stringify(snap)) as Snapshot
  const t06 = done.workstreams.find((w) => w.bead_id === 'example-app-t06')!
  Object.assign(t06, { state: 'in-progress', steps: closeAll(t06.steps), owner: undefined })
  done.needs_you = done.needs_you.filter((n) => !n.bead_id.startsWith('example-app-t06'))
  // cmdrd's board for it: done, nothing open.
  done.display = { ...done.display!, asks: done.display!.asks.filter((a) => !a.key.includes('example-app-t06')), workstreams: done.display!.workstreams.map((w) => (w.bead_id === 'example-app-t06' ? { ...w, kind: 'done', text: 'Ready to complete', flag: 0 } : w)) }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/api/cmd')) {
        bodies.push(JSON.parse(String(init?.body)))
        return new Response(JSON.stringify({ ok: true, stdout: '{"bead_id":"example-app-t06","closed":true}\n', stderr: '', exit: 0 }))
      }
      return new Response('{}', { status: 404 })
    }),
  )
  useStore.setState({ snapshot: done, config: { apiUrl: 'http://127.0.0.1:9', token: 't' }, selected: { kind: 'commander' }, cursor: 'commander', completed: {}, leaving: {}, completeError: {} })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

it('closes a finished workstream on the user’s click', async () => {
  render(<Navigator />)
  const buttons = screen.getAllByRole('button', { name: '✓ Complete' })
  expect(buttons).toHaveLength(1)
  await userEvent.click(buttons[0])
  // The row leaves the sidebar once its exit has played.
  await waitFor(() => expect(screen.queryByText('Prompt experiments')).not.toBeInTheDocument(), { timeout: 2000 })
  expect(useStore.getState().completed['example-app:example-app-t06']).toBeGreaterThan(0)
  expect(bodies).toEqual([{ argv: ['internal-close-objective', '--project', 'example-app', '--bead', 'example-app-t06'] }])
})

it('says it failed when the request is cut off, instead of closing forever', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Failed to fetch') }))
  render(<Navigator />)
  await userEvent.click(screen.getByRole('button', { name: '✓ Complete' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Retry Complete' })).toBeInTheDocument())
})

it('counts a close that finds it already closed as done', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: false, exit: 1, stdout: '', stderr: '{"error":"br close: exit status 3: Warning: Skipped example-app-t06: already closed"}' }))))
  render(<Navigator />)
  await userEvent.click(screen.getByRole('button', { name: '✓ Complete' }))
  await waitFor(() => expect(screen.queryByText('Prompt experiments')).not.toBeInTheDocument(), { timeout: 2000 })
  expect(screen.queryByRole('button', { name: 'Retry Complete' })).not.toBeInTheDocument()
})
