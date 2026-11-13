import { useState } from 'react'
import { flushSync } from 'react-dom'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BoardRow, Snapshot, SnapWorkstream } from '../src/shared/snapshot'
import { Overview, OverviewPage, overviewAsks, overviewTickets, WorkstreamHeader, type RecapResult } from '../src/renderer/src/components/Overview'
import { SourceReader, sourcePage } from '../src/renderer/src/components/OverviewSource'
import { InspectorColumn } from '../src/renderer/src/components/InspectorColumn'
import { ConfirmDialog } from '../src/renderer/src/components/ConfirmDialog'
import { useIntegrations } from '../src/features/linear/settings'
import { useStore } from '../src/renderer/src/store'

const ws: SnapWorkstream = { n: 1, project: 'p', project_id: 1, bead_id: 'w', title: 'Ship dashboards', state: 'in-progress', status: 'open', manager_role: 'manager-w', done: 1, total: 3, repo: 'org/repo', steps: [
  { bead_id: 'track-a', title: 'Dashboards', state: 'in-progress', status: 'open', track: true, children: [{ bead_id: 'apply', title: 'Apply Terraform', state: 'needs-user', status: 'open', pr: ['3399'] }] },
  { bead_id: 'track-b', title: 'Payroll', state: 'in-progress', status: 'open', track: true },
] }
const row: BoardRow = { n: 1, key: 'p:w', project: 'p', bead_id: 'w', title: ws.title, manager_role: 'manager-w', starred: false, kind: 'working', text: 'Verifying the dashboard', since: 0, progress: { done: 1, total: 3 }, working: 1, flag: 0, unread: 0, gate: null, tracks: [
  { key: 'a', bead_id: 'track-a', letter: 'A', title: 'Dashboards', kind: 'waiting', text: 'Waiting for the infrastructure apply', since: 0, progress: { done: 1, total: 2 }, working: 0 },
  { key: 'b', bead_id: 'track-b', letter: 'B', title: 'Payroll', kind: 'working', text: 'Payroll migrating', since: 0, progress: { done: 0, total: 1 }, working: 1 },
] }
const snap: Snapshot = { version: 1, generated_at: new Date().toISOString(), projects: [], workstreams: [ws], needs_you: [], agents: {}, fleet: {} as Snapshot['fleet'], unread: {}, display: { commander: { kind: 'idle', text: '', since: 0, unread: 0 }, asks: [], workstreams: [row] } }
const result: RecapResult = { state: 'ready', stale: false, evidence: [{ id: 'apply', label: 'Terraform state', text: 'Manual apply remains pending in two workspaces.' }], recap: { generated_at: new Date().toISOString(), now: { sentences: [{ text: 'The dashboard button is published; the infrastructure apply is still pending.', sources: ['apply'] }] }, next: { sentences: [{ text: 'Verify access after the apply completes.', sources: ['apply'] }] } } }

beforeEach(() => {
  useIntegrations.setState({ linearTeam: 'TEAM' }) // the titles' ticket prefix
  useStore.setState({ snapshot: structuredClone(snap), config: { apiUrl: 'http://localhost:8787', token: 't' }, conn: 'live', selected: { kind: 'ws', key: 'p:w' }, workflowOpen: null, narrow: false, chatTab: false })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).includes('/api/overview') ? result : { project_id: 1, ws: 'w', active_id: 0, tabs: [] }), { status: 200 })))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); useIntegrations.setState({ linearTeam: '' }) })

describe('Overview', () => {
  it('renders a null or empty streaming draft without throwing', async () => {
    const draft = { state: 'updating', stale: false, evidence: [], draft: { now: { sentences: null }, recent: null, next: null } }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => new Response(JSON.stringify(String(input).includes('/api/overview') ? draft : { project_id: 1, ws: 'w', active_id: 0, tabs: [] }), { status: 200 })))
    render(<InspectorColumn />)
    expect(await screen.findByRole('article', { name: 'Workstream overview' })).toBeInTheDocument()
    expect(screen.queryByText('The summary could not be shown.')).toBeNull()
  })
  it('opens by default, keeps Workflow available, and does not move tabs on a snapshot', async () => {
    render(<InspectorColumn />)
    expect(await screen.findByRole('article', { name: 'Workstream overview' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'true')
    fireEvent.click(screen.getByRole('tab', { name: 'Workflow' }))
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Workflow' })).toHaveAttribute('aria-selected', 'true'))
    useStore.getState().setSnapshot({ ...snap, version: 2 })
    expect(screen.getByRole('tab', { name: 'Workflow' })).toHaveAttribute('aria-selected', 'true')
  })
  it('is one page: one summary for both parts, in order, with a contents list once both have words', async () => {
    const asked: string[] = []
    const both = { ...result, stale: true, recap: { ...result.recap!, problem: { sentences: [{ text: 'Dashboards are hard to find.', sources: [] }] } } }
    vi.stubGlobal('fetch', vi.fn(async (_: RequestInfo | URL, init?: RequestInit) => { asked.push(JSON.parse(String(init?.body)).part); return new Response(JSON.stringify(both), { status: 200 }) }))
    const parts = () => screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent!.trim()).filter((t) => t === 'Current status' || t === 'Pitch')
    const { unmount } = render(<OverviewPage ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    await screen.findByText(/The dashboard button is published/)
    expect(parts()).toEqual(['Current status', 'Pitch'])
    expect(within(screen.getByRole('navigation', { name: 'On this page' })).getAllByRole('button').map((b) => b.textContent!.trim())).toEqual(['Current status', 'Pitch'])
    expect(new Set(asked)).toEqual(new Set(['']))
    // a stale summary stays on screen, whole, while a newer one is written
    expect(screen.queryByText(/Refreshing summary/)).toBeNull()
    unmount()
    render(<OverviewPage ws={{ ...ws, phase: 'scoping' }} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    await screen.findByText(/The dashboard button is published/)
    expect(parts()).toEqual(['Pitch', 'Current status'])
  })
  it('shows only a part that has words, and no contents list for one part', async () => {
    render(<OverviewPage ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    await screen.findByText(/The dashboard button is published/)
    expect(screen.queryByRole('heading', { name: 'Pitch' })).toBeNull()
    expect(screen.queryByRole('navigation', { name: 'On this page' })).toBeNull()
  })
  it('is one loader and nothing else until the first summary has words', async () => {
    const waiting = { state: 'updating', stale: false, evidence: [], draft: { now: { sentences: [] } } }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(waiting), { status: 200 })))
    render(<OverviewPage ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(await screen.findAllByText(/Refreshing summary/)).toHaveLength(1)
    expect(screen.queryAllByRole('heading', { level: 2 })).toHaveLength(0)
    expect(screen.queryByRole('link', { name: /PR #/ })).toBeNull()
    expect(screen.queryByRole('navigation', { name: 'On this page' })).toBeNull()
  })
  it('shows the tickets under a clean title, a workstream counting its open tracks', () => {
    const carrying: SnapWorkstream = { ...ws, title: 'Ship dashboards (TEAM-1)', steps: [{ ...ws.steps![0], title: 'Dashboards (TEAM-2)' }, { ...ws.steps![1], tickets: ['TEAM-3'] }] }
    expect(overviewTickets(carrying)).toEqual(['TEAM-1', 'TEAM-2', 'TEAM-3'])
    expect(overviewTickets(carrying, 'track-a')).toEqual(['TEAM-2'])
    render(<WorkstreamHeader ws={carrying} row={{ ...row, title: carrying.title }} />)
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(/^Ship dashboards$/)
    expect(screen.getByText(/TEAM-1/)).toBeInTheDocument()
  })
  it('shows the narrative and real artifact links, with no citation numbers', async () => {
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(await screen.findByText(/The dashboard button is published/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'PR #3399 ↗' })).toHaveAttribute('href', 'https://github.com/org/repo/pull/3399')
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
  })
  it('reads each cited sentence clean, without source numbers', async () => {
    const two = { ...result, evidence: [...result.evidence, { id: 'pr', label: 'The PR', text: 'Open.' }], recap: { ...result.recap!, now: { sentences: [{ text: 'The button is published.', sources: ['pr'] }, { text: 'The apply is pending.', sources: ['apply'] }] }, next: { sentences: [{ text: 'Verify access.', sources: ['apply'] }] } } }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(two), { status: 200 })))
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    await screen.findByText(/The button is/)
    expect(screen.getByText(/The button is/).closest('p')!.textContent).toBe('The button is published. The apply is pending.')
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
  })
  it('still reads a recap from a daemon that sends one block of sources', async () => {
    const old = { ...result, recap: { ...result.recap!, now: { text: 'Old recap sentence.', sources: ['apply'] }, next: undefined } }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(old), { status: 200 })))
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(await screen.findByText(/Old recap/)).toBeInTheDocument()
  })
  it('keeps the live status in the header when nothing needs the user, without an empty attention block', () => {
    render(<><WorkstreamHeader ws={ws} row={row} /><Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} /></>)
    expect(screen.getByText('Verifying the dashboard')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Needs you' })).toBeNull()
  })
  it('opens a board step as a page to read, never as its raw record', () => {
    const step = { title: 'Fix merged (TEAM-12)', state: 'blocked', status: 'open', note: '**The problem:** the deploy fails.', waiting: 'operator-op-merge-1', working: false, owner_state: '', owner_why: '', review: null, next_after: ['known', 'unknown'], gates: [{ bead_id: 'g', name: 'merge-approved', verifier: 'user', status: 'passed' }, { bead_id: 'h', name: 'live', verifier: 'operator', status: 'open' }] }
    const page = sourcePage({ id: 's', label: 'Fix merged (TEAM-12)', text: JSON.stringify(step) }, new Map([['known', 'Greenlight the merge']]))
    expect(page).toEqual({
      title: 'Fix merged',
      status: { text: 'Waiting on an agent', tone: 'waiting' },
      lead: undefined,
      body: '**The problem:** the deploy fails.',
      checks: [{ name: 'Merge approved', detail: 'Passed, checked by you', passed: true, failed: false }, { name: 'Live', detail: 'Waiting on an agent', passed: false, failed: false }],
      after: ['Greenlight the merge', '1 more step not named here'],
    })
    render(<SourceReader source={{ id: 's', label: 'Fix merged (TEAM-12)', text: JSON.stringify(step) }} titles={new Map()} onClose={vi.fn()} />)
    const reader = screen.getByRole('dialog', { name: 'Supporting evidence' })
    expect(within(reader).getByText('the deploy fails.', { exact: false })).toBeInTheDocument()
    expect(reader.textContent).not.toMatch(/next_after|owner_state|\{"/)
  })
  it('reads a status and a Manager report in plain language', () => {
    expect(sourcePage({ id: 'live-status', label: 'Current verified board status', text: '{"kind":"working","text":"Reviewing the PR","why":"","working":1}' }, new Map())).toEqual({ title: 'Current verified board status', lead: 'Reviewing the PR', body: undefined, checks: [], after: [] })
    expect(sourcePage({ id: 'chat:1', label: 'Manager report (not independently verified)', text: 'The **agent** is running.', at: '2026-11-05T03:19:25Z' }, new Map())).toEqual({ title: 'Manager report', caveat: 'Not independently verified', body: 'The **agent** is running.', checks: [], after: [] })
  })
  it('uses actual decision cards and opens the existing decision flow', () => {
    const ask = { project: 'p', bead_id: 'apply', title: 'Apply Terraform', workstream: ws.title, rank: 1, track_id: 'track-a', decision: { question: 'Who will apply the two Terraform plans?', context: 'The automation will not apply this policy change.' } }
    useStore.setState({ snapshot: { ...snap, needs_you: [ask] } })
    render(<Overview ws={ws} row={row} track="track-a" onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(screen.getByRole('heading', { name: ask.decision.question })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Answer this' }))
    expect(useStore.getState().selected).toEqual({ kind: 'ask', key: 'ask:p:apply' })
  })
  it('keeps other-track decisions out and preserves cards whose step is absent from the projection', () => {
    const ask = { project: 'p', bead_id: 'missing-child', title: 'Apply Terraform', workstream: ws.title, rank: 1, track_id: 'track-a' }
    const s = { ...snap, needs_you: [ask] }
    expect(overviewAsks(s, ws, 'track-a')).toHaveLength(1)
    expect(overviewAsks(s, ws, 'track-b')).toHaveLength(0)
  })
  it('distinguishes a stalled agent from a user decision', () => {
    const stalled: BoardRow = { ...row, kind: 'stalled', working: 0, text: 'Operator stopped without reporting' }
    render(<><WorkstreamHeader ws={ws} row={stalled} /><Overview ws={ws} row={stalled} onWorkflow={vi.fn()} onTrack={vi.fn()} /></>)
    expect(screen.getByText('No decision is requested from you. Agent recovery is needed.')).toBeInTheDocument()
    expect(screen.getByText('Operator stopped without reporting')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Status' })).toHaveClass('ov-stalled')
    expect(screen.queryByRole('region', { name: 'Needs you' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Answer this' })).toBeNull()
  })
  it('keeps a stale recap on screen under the loader while the reader retries', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ...result, stale: true, state: 'retrying' }), { status: 200 })))
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(await screen.findByText(/The dashboard button is published/)).toBeInTheDocument()
    expect(screen.getByText(/Refreshing summary/)).toBeInTheDocument()
  })
  it('shows the one loader while the board loads and never reads unavailable', async () => {
    const waiting = { state: 'waiting', stale: false, evidence: [], waiting_for: 'the board to finish loading (cmdrd is still reading its first snapshot)' }
    const calls: number[] = []
    vi.stubGlobal('fetch', vi.fn(async () => { calls.push(1); return calls.length === 1 ? new Response(JSON.stringify(waiting), { status: 200 }) : new Response('x', { status: 503 }) }))
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    await waitFor(() => expect(calls.length).toBeGreaterThan(0))
    expect(screen.getByText(/Refreshing summary/)).toBeInTheDocument()
    expect(screen.queryByText(/unavailable/i)).toBeNull()
  })
  it('streams a sentence in word by word, never redrawing what shows, and settles it once finished', async () => {
    const enc = new TextEncoder()
    const frame = (text: string, partial: boolean) => JSON.stringify({ state: 'updating', stale: false, evidence: result.evidence, draft: { now: { sentences: [{ text, sources: partial ? [] : ['apply'], partial }] } } }) + '\n'
    let ctl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start(c) { ctl = c } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status: 200 })))
    const { container } = render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    ctl.enqueue(enc.encode(frame('The dashboard ', true)))
    const first = await screen.findByText('The')
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
    ctl.enqueue(enc.encode(frame('The dashboard button is pub', true)))
    await screen.findByText(/pub/)
    expect(screen.getByText('The')).toBe(first) // the drawn word is the same node, not redrawn
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
    ctl.enqueue(enc.encode(frame('The dashboard button is published.', false)))
    await waitFor(() => expect(container.querySelector('.ov-cited')).toHaveTextContent('published.'))
    expect(screen.queryByRole('button', { name: /Evidence/ })).toBeNull()
    ctl.close()
  })
  it('keeps an earlier recap on screen while a rewrite is written, then swaps the new one in whole', async () => {
    const enc = new TextEncoder()
    const earlier = JSON.stringify({ ...result, state: 'updating' }) + '\n'
    const frame = (text: string) => JSON.stringify({ state: 'updating', stale: false, evidence: result.evidence, recap: result.recap, draft: { now: { sentences: [{ text, sources: [], partial: true }] } } }) + '\n'
    const rewritten = { ...result, recap: { ...result.recap!, generated_at: new Date(Date.now() + 1000).toISOString(), now: { sentences: [{ text: 'Zebra crossings are painted.', sources: ['apply'] }] } } }
    let ctl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start(c) { ctl = c } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status: 200 })))
    const { container } = render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    ctl.enqueue(enc.encode(earlier))
    expect(await screen.findByText(/The dashboard button is published/)).toBeInTheDocument()
    ctl.enqueue(enc.encode(frame('Zebra crossings are now ')))
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByText(/Zebra/)).toBeNull()
    expect(screen.getByText(/The dashboard button is published/)).toBeInTheDocument()
    ctl.enqueue(enc.encode(JSON.stringify(rewritten) + '\n'))
    expect(await screen.findByText(/Zebra crossings are/)).toBeInTheDocument()
    expect(screen.queryByText(/The dashboard button is published/)).toBeNull()
    expect(container.querySelector('.ov-streaming')).toBeNull()
    ctl.close()
  })
  it('streams the recap in as it is written', async () => {
    const draft = { state: 'updating', stale: true, evidence: result.evidence, draft: { now: { sentences: [{ text: 'The button is published.', sources: ['apply'] }] } } }
    const enc = new TextEncoder()
    let finish: () => void = () => {}
    const body = new ReadableStream<Uint8Array>({ start(c) {
      c.enqueue(enc.encode(JSON.stringify(draft) + '\n'))
      finish = () => { c.enqueue(enc.encode(JSON.stringify(result) + '\n')); c.close() }
    } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status: 200 })))
    render(<Overview ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(await screen.findByText(/The button is/)).toBeInTheDocument()
    expect(screen.queryByText(/Refreshing summary/)).toBeNull() // the draft stands in for the loader
    finish()
    expect(await screen.findByText(/The dashboard button is published/)).toBeInTheDocument()
    expect(screen.queryByText(/The button is/)).toBeNull()
  })
})

describe('a source page reads as the board does', () => {
  const page = (record: object, titles = new Map<string, string>()) => sourcePage({ id: 'step-1', label: 'Fix merged', text: JSON.stringify(record) }, titles)
  const reader = (text: string, id = 'chat:1') => render(<SourceReader source={{ id, label: 'Manager report (not independently verified)', text }} titles={new Map()} onClose={vi.fn()} />)

  it('reads a failed check as failed, with its evidence, who and when', () => {
    const at = Date.UTC(2026, 10, 5, 12) / 1000
    const gate = { name: 'ci', verifier: 'auto', status: 'failed', evidence: 'build red', by: 'operator-op-merge-1', at }
    const [check] = page({ status: 'open', gates: [gate] }).checks
    expect(check.detail).toMatch(/^Failed, build red \(checked by an agent, Nov \d+, .+\)$/)
    expect(check).toMatchObject({ name: 'Ci', passed: false, failed: true })
    expect(page({ status: 'open', gates: [{ name: 'ci', verifier: 'auto', status: 'failed' }] }).checks[0].detail).toBe('Failed')
    render(<SourceReader source={{ id: 'step-1', label: 'Fix merged', text: JSON.stringify({ status: 'open', gates: [gate] }) }} titles={new Map()} onClose={vi.fn()} />)
    expect(screen.getByText(/^Failed, build red/).closest('li')).toHaveClass('is-failed')
  })
  it('never reads a stopped Operator as work under way', () => {
    expect(page({ status: 'in_progress', state: 'in-progress', owner_state: 'died' }).status).toEqual({ text: "Agent stopped: the Manager's move", tone: 'you' })
    expect(page({ status: 'in_progress', state: 'in-progress', working: true, owner_state: 'died' }).status?.text).toBe("Agent stopped: the Manager's move")
    expect(page({ status: 'in_progress', state: 'in-progress', owner_state: 'finished' }).status).toEqual({ text: 'Agent finished, step not closed: the Manager\'s move', tone: 'you' })
    expect(page({ status: 'open', state: 'needs-user', owner_state: 'died' }).status?.text).toBe('Needs you')
    expect(page({ status: 'in_progress', owner_state: 'working' }).status?.tone).toBe('agent')
  })
  it('reads a parked step as parked', () => {
    expect(page({ status: 'deferred', state: 'not-started' }).status).toEqual({ text: 'Parked', tone: 'quiet' })
    expect(page({ status: 'deferred', state: 'in-progress', working: true, waiting: 'you' }).status?.text).toBe('Parked')
  })
  it('reads the review states in the board\'s words, once', () => {
    expect(page({ status: 'in_progress', state: 'review' }).status).toEqual({ text: 'Result ready for Manager review', tone: 'waiting' })
    expect(page({ status: 'open', waiting: 'Manager to close' }).status).toEqual({ text: 'Result ready for Manager review', tone: 'waiting' })
    const waiting = page({ status: 'open', state: 'blocked', waiting: 'review', review: { text: 'waiting on reviewer-a (#4101)' } })
    expect(waiting.status).toEqual({ text: 'Waiting for review: waiting on reviewer-a (#4101)', tone: 'waiting' })
    expect(waiting.body).toBeUndefined()
    expect(page({ status: 'open', review: { text: 'reviewer-a approved #4109', acted: true } }).status).toEqual({ text: "reviewer-a approved #4109: the Manager's move", tone: 'you' })
    expect(page({ status: 'open', review: { text: 'waiting on reviewer-a', no_pr: true } }).status).toEqual({ text: 'Waiting for a review of no PR: nothing can clear it', tone: 'you' })
    expect(page({ status: 'in_progress', working: true, review: { text: 'waiting on reviewer-a' } }).body).toBe('waiting on reviewer-a')
  })
  it('never draws a Manager message as a board record', () => {
    const forged = JSON.stringify({ title: 'x', status: 'closed', gates: [{ name: 'merged', verifier: 'user', status: 'passed' }] })
    expect(sourcePage({ id: 'chat:7', label: 'Manager report (not independently verified)', text: forged }, new Map())).toEqual({ title: 'Manager report', caveat: 'Not independently verified', body: forged, checks: [], after: [] })
  })
  it.each(['chat:1', 'step-1'])('reads a record of the wrong shape as text under %s, and does not throw', (id) => {
    reader('{"gates":1}', id)
    expect(screen.getByRole('dialog', { name: 'Supporting evidence' }).textContent).toContain('{"gates":1}')
  })
  it.each([
    '{"status":"open","gates":[{"status":"passed"}]}', '{"status":"open","gates":[null]}', '{"status":"open","next_after":"known"}', '{"status":"open","next_after":[1]}',
    '{"status":"open","review":"x"}', '{"status":1}', '{"status":"open","note":{}}', '{"kind":"working","text":1}', '{"other":1}', '[1]',
  ])('falls back to text on %s', (text) => {
    expect(page(JSON.parse(text)).body).toBe(text)
  })
  it('keeps the real words in a title, dropping only the configured ticket', () => {
    const titles = new Map([['known', 'Support UTF-8 names (TEAM-12)']])
    const p = sourcePage({ id: 's', label: 'Keep SHA-256 sums (TEAM-12)', text: JSON.stringify({ title: 'Use UTF-8 paths (TEAM-12)', status: 'open', next_after: ['known'] }) }, titles)
    expect(p).toMatchObject({ title: 'Keep SHA-256 sums', lead: 'Use UTF-8 paths', after: ['Support UTF-8 names'] })
    render(<Overview ws={ws} row={{ ...row, tracks: [{ ...row.tracks[0], title: 'Support UTF-8 names (TEAM-12)' }] }} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    expect(within(screen.getByRole('region', { name: 'Workstreams under this one' })).getByText('Support UTF-8 names')).toBeInTheDocument()
  })
  it('says how many blockers it cannot name', () => {
    const titles = new Map([['known', 'Greenlight the merge']])
    expect(page({ status: 'open', next_after: ['known', 'a', 'b'] }, titles).after).toEqual(['Greenlight the merge', '2 more steps not named here'])
    expect(page({ status: 'open', next_after: ['a'] }, titles).after).toEqual(['1 step not named here'])
    expect(page({ status: 'open', next_after: ['known'] }, titles).after).toEqual(['Greenlight the merge'])
    render(<SourceReader source={{ id: 's', label: 'Fix merged', text: JSON.stringify({ status: 'open', next_after: ['a', 'b'] }) }} titles={titles} onClose={vi.fn()} />)
    expect(within(screen.getByRole('region', { name: 'Comes after' })).getByText('2 steps not named here')).toBeInTheDocument()
  })
  it('keeps raw HTML as text and gives javascript: and file: links no address', () => {
    const { container } = reader('<img src=x onerror=alert(1)> <script>alert(2)</script>\n\n[run](javascript:alert(1)) [file](file:///etc/passwd) [ok](https://example.com/a)')
    const body = container.querySelector('.ov-reader-body')!
    expect(body.querySelector('script, img[onerror]')).toBeNull()
    expect(body.textContent).toContain('<img src=x onerror=alert(1)>')
    expect(within(body as HTMLElement).getByText('run')).not.toHaveAttribute('href', expect.stringMatching(/javascript/))
    expect(within(body as HTMLElement).getByText('file')).not.toHaveAttribute('href', expect.stringMatching(/file:|passwd/))
    expect(within(body as HTMLElement).getByRole('link', { name: 'ok' })).toHaveAttribute('href', 'https://example.com/a')
  })
  describe('Escape closes the top layer only', () => {
    const source = { id: 's', label: 'Fix merged', text: 'A note.' }
    // The dialog's own listener is registered first, so it goes first unless the reader listens earlier.
    function Layers({ onCloseReader }: { onCloseReader: () => void }) {
      const [dialog, setDialog] = useState(true)
      return <>
        {dialog && <ConfirmDialog title="Sure?" confirm="Yes" onConfirm={() => undefined} onCancel={() => flushSync(() => setDialog(false))}>Really?</ConfirmDialog>}
        <SourceReader source={source} titles={new Map()} onClose={onCloseReader} />
      </>
    }
    it('leaves the reader open under a dialog, then closes it', () => {
      const onClose = vi.fn()
      render(<Layers onCloseReader={onClose} />)
      fireEvent.keyDown(document.body, { key: 'Escape' })
      expect(screen.queryByRole('dialog', { name: 'Sure?' })).toBeNull()
      expect(onClose).not.toHaveBeenCalled()
      fireEvent.keyDown(document.body, { key: 'Escape' })
      expect(onClose).toHaveBeenCalledTimes(1)
    })
    it('leaves the reader open under the health popover', () => {
      const onClose = vi.fn()
      const pop = document.body.appendChild(Object.assign(document.createElement('div'), { className: 'health-pop' }))
      render(<SourceReader source={source} titles={new Map()} onClose={onClose} />)
      fireEvent.keyDown(document.body, { key: 'Escape' })
      expect(onClose).not.toHaveBeenCalled()
      pop.remove()
      fireEvent.keyDown(document.body, { key: 'Escape' })
      expect(onClose).toHaveBeenCalledTimes(1)
    })
  })
})

describe("Overview targets", () => {
  it("shows the goal targets as a table, one row each", async () => {
    const withTargets = { ...result, recap: { ...result.recap!, goal: { sentences: [{ text: "Every endpoint at most 500 ms.", sources: ["apply"] }] }, targets: [{ name: "Worklist (Needs Review)", target: "≤ 500 ms", was: "20.2 s", now: "726 ms", met: false }, { name: "Worklist counts", target: "≤ 500 ms", was: "3.4 s", now: "346 ms", met: true }] } }
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(withTargets), { status: 200 })))
    render(<OverviewPage ws={ws} row={row} onWorkflow={vi.fn()} onTrack={vi.fn()} />)
    const table = await screen.findByRole("table")
    expect(within(table).getAllByRole("row")).toHaveLength(3)
    expect(within(table).getByText("726 ms")).toBeInTheDocument()
    expect(within(table).getByLabelText("Not met")).toBeInTheDocument()
  })
})
