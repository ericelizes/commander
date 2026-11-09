import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { BoardRow, Snapshot, SnapWorkstream } from '../src/shared/snapshot'
import { InspectorColumn } from '../src/renderer/src/components/InspectorColumn'
import { useStore } from '../src/renderer/src/store'

const ws: SnapWorkstream = { n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'Ship', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 1, total: 3, steps: [] }
const row: BoardRow = { n: 1, key: 'p:w', project: 'p', bead_id: 'w', title: 'Ship', manager_role: 'manager-w', starred: false, kind: 'working', text: 'Working', since: 0, progress: { done: 1, total: 3 }, working: 1, flag: 0, unread: 0, gate: null, tracks: [] }
const ask = { project: 'p', bead_id: 'w.open', title: 'Approve the apply?', workstream: 'Ship', rank: 1 }
const snap = { version: 1, generated_at: new Date().toISOString(), projects: [], workstreams: [ws], needs_you: [ask], agents: {}, fleet: {}, unread: {}, display: { commander: { kind: 'idle', text: '', since: 0, unread: 0 }, asks: [], workstreams: [row] } } as unknown as Snapshot
const tab = (id: number, kind: string, target: string, title: string) => ({ id, kind, target, title, view: kind === 'decision' ? 'decision' : 'web', added_by: 'operator-x' })
const tabs = [tab(1, 'decision', 'w.done', '⚑ w.done'), tab(2, 'decision', 'w.open', '⚑ w.open'), tab(3, 'url', 'https://x.example/', 'A page')]

beforeEach(() => {
  localStorage.clear()
  useStore.setState({ snapshot: structuredClone(snap), config: { apiUrl: 'http://localhost:8787', token: 't' }, conn: 'live', selected: { kind: 'ws', key: 'p:w' }, workflowOpen: null, pendingPage: null, narrow: false, chatTab: false })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ project_id: 1, ws: 'w', active_id: 3, tabs, state: 'waiting', evidence: [] }), { status: 200 })))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

it('drops finished decision tabs and names open ones by their question', async () => {
  render(<InspectorColumn />)
  await waitFor(() => expect(screen.getByRole('tab', { name: /Approve the apply/ })).toBeInTheDocument())
  expect(screen.getByRole('tab', { name: /A page/ })).toBeInTheDocument()
  expect(screen.queryByText(/w\.done/)).toBeNull()
  expect(screen.queryByText(/w\.open/)).toBeNull()
  expect(screen.queryByText(/⚑ /)).toBeNull()
})

it('shows no tab for the decision already open: it reads on Overview', async () => {
  useStore.setState({ selected: { kind: 'ask', key: 'ask:p:w.open' } })
  render(<InspectorColumn />)
  await waitFor(() => expect(screen.getByRole('tab', { name: /A page/ })).toBeInTheDocument())
  expect(screen.queryByRole('tab', { name: /Approve the apply/ })).toBeNull()
})
