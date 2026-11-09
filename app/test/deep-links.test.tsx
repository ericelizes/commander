import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BoardRow, Snapshot, SnapWorkstream } from '../src/shared/snapshot'
import { InspectorColumn } from '../src/renderer/src/components/InspectorColumn'
import { useStore } from '../src/renderer/src/store'
import { linkLabel, linkTarget } from '../src/renderer/src/viewmodel'

const ws: SnapWorkstream = { n: 2, project: 'p', project_id: 1, bead_id: 'w', title: 'Ship dashboards', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 1, total: 3, steps: [
  { bead_id: 'w.1', title: 'Dashboards', state: 'in-progress', status: 'open', track: true, children: [{ bead_id: 'w.1.1', title: 'Apply Terraform', state: 'in-progress', status: 'open' }, { bead_id: 'w.1.2', title: 'Approve the apply', state: 'needs-user', status: 'open' }] },
  { bead_id: 'w.2', title: 'Plain step', state: 'not-started', status: 'open' },
] }
const row: BoardRow = { n: 2, key: 'p:w', project: 'p', bead_id: 'w', title: ws.title, manager_role: 'manager-w', starred: false, kind: 'working', text: 'Working', since: 0, progress: { done: 1, total: 3 }, working: 1, flag: 0, unread: 0, gate: null, tracks: [{ key: 'a', bead_id: 'w.1', letter: '.1', title: 'Dashboards', kind: 'working', text: 'Applying', since: 0, progress: { done: 0, total: 1 }, working: 1 }] }
const ask = { project: 'p', bead_id: 'w.1.2', title: 'Approve the apply', workstream: ws.title, rank: 1, track_id: 'w.1', decision: { question: 'Apply it?', context: '' } }
const snap = { version: 1, generated_at: new Date().toISOString(), projects: [], workstreams: [ws], needs_you: [ask], agents: {}, fleet: {}, unread: {}, display: { commander: { kind: 'idle', text: '', since: 0, unread: 0 }, asks: [], workstreams: [row] } } as unknown as Snapshot

beforeEach(() => {
  localStorage.clear()
  useStore.setState({ snapshot: structuredClone(snap), config: { apiUrl: 'http://localhost:8787', token: 't' }, conn: 'live', selected: { kind: 'commander' }, workflowOpen: null, pendingPage: null, narrow: false, chatTab: false })
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ project_id: 1, ws: 'w', active_id: 0, tabs: [], state: 'waiting', evidence: [] }), { status: 200 })))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('where a link goes', () => {
  it('finds a bead\'s workstream and track, and only an exact decision', () => {
    expect(linkTarget(snap, 'w')).toMatchObject({ track: undefined, ask: undefined })
    expect(linkTarget(snap, 'w.1.1')).toMatchObject({ track: 'w.1', letter: '.1', ask: undefined })
    expect(linkTarget(snap, 'w.1.2')?.ask?.bead_id).toBe('w.1.2')
    expect(linkTarget(snap, 'gone')).toBeNull()
  })
  it('says where it goes in the user\'s terms', () => {
    expect(linkLabel(snap, 'w')).toBe('Workstream 2, Overview')
    expect(linkLabel(snap, 'w.1.1', 'workflow')).toBe('Workstream 2.1, Workflow')
    expect(linkLabel(snap, 'w.1.2')).toBe('Workstream 2.1, Overview')
    expect(linkLabel(snap, 'w.1.2', 'decision')).toBe('Workstream 2.1, Overview')
    expect(linkLabel(snap, 'gone')).toBe('Not on the board right now')
  })
  it('a workstream link opens that workstream even with a decision under it', () => {
    useStore.getState().openLink('w')
    expect(useStore.getState().selected).toEqual({ kind: 'ws', key: 'p:w' })
    expect(useStore.getState().workflowOpen).toMatchObject({ ws: 'p:w', track: undefined, tab: 'overview' })
  })
  it('a track and tab link opens that track on that tab', async () => {
    render(<InspectorColumn />)
    useStore.getState().openLink('w.1.1', 'workflow')
    expect(useStore.getState().workflowOpen).toMatchObject({ ws: 'p:w', track: 'w.1', tab: 'workflow' })
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Workflow' })).toHaveAttribute('aria-selected', 'true'))
  })
  it('a decision opens on its Overview, with no tab of its own', async () => {
    render(<InspectorColumn />)
    useStore.getState().openLink('w.1.2')
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true'))
    expect(screen.queryByRole('tab', { name: /Approve the apply/ })).toBeNull()
    expect(useStore.getState().selected).toEqual({ kind: 'ask', key: expect.any(String) })
    expect(screen.getByRole('tab', { name: 'Workflow' })).toBeInTheDocument()
  })
  it('a page link goes to its workstream, then opens the page there', async () => {
    const seen: unknown[] = []
    const claim = (e: Event) => { e.preventDefault(); seen.push((e as CustomEvent).detail) }
    window.addEventListener('cmdr:open-tab', claim)
    try {
      render(<InspectorColumn />)
      expect(useStore.getState().openLinkPage('w.1.1', 'https://x.example/pr', 'PR')).toBe(true)
      await waitFor(() => expect(seen).toEqual([{ url: 'https://x.example/pr', title: 'PR' }]))
      expect(useStore.getState().selected).toEqual({ kind: 'ws', key: 'p:w' })
      expect(useStore.getState().pendingPage).toBeNull()
      expect(useStore.getState().openLinkPage('gone', 'https://x.example/pr')).toBe(false)
    } finally {
      window.removeEventListener('cmdr:open-tab', claim)
    }
  })
})
