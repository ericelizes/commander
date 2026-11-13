// The integrated renderer: NEEDS YOU queue in the navigator opens the ask's
// decision card in the inspector column, which submits through /api/cmd;
// a workstream shows its tabs (web mode: no commanderHost); the chat slot
// falls back when cmdrd has no chat engine.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { AskDock } from '../src/renderer/src/components/AskDock'
import { InspectorColumn } from '../src/renderer/src/components/InspectorColumn'
import { Rail } from '../src/renderer/src/components/Rail'
import { LinearPill } from '../src/features/linear/Ticket'
import { useIntegrations } from '../src/features/linear/settings'
import { ChatSlot } from '../src/renderer/src/components/ChatSlot'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const cfg = { apiUrl: 'http://127.0.0.1:9', token: 't' }

type Call = { url: string; method: string; body?: unknown }
let calls: Call[]

function fakeFetch(chatStatus = 404) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.endsWith('/api/snapshot')) return json(200, snap)
    if (url.endsWith('/api/cmd')) return json(200, { ok: true, stdout: '', stderr: '', exit: 0 })
    if (url.includes('/api/tabs')) return json(200, { project_id: 1, ws: '', active_id: 0, tabs: [] })
    if (url.endsWith('/api/chat/commander/send')) return json(200, { ok: true })
    if (url.endsWith('/api/chat')) return json(chatStatus, chatStatus === 200 ? { ok: true, sessions: [] } : { error: 'no' })
    return json(404, {})
  })
}

beforeEach(() => {
  localStorage.removeItem('nav.tab')
  useIntegrations.setState({ linearWorkspace: '' })
  calls = []
  vi.stubGlobal('fetch', fakeFetch())
  useStore.setState({ snapshot: snap, config: cfg, selected: { kind: 'commander' }, cursor: 'commander' })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('integrated renderer', () => {
  it('loads the local workspace through settings and updates ticket links', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true, settings: {
      user_name: 'Alice', linear_workspace: 'acme', codex_manager_share: 0.5, codex_operator_share: 0.5,
    } }), { status: 200 })))
    render(<><Rail /><LinearPill id="ACME-7" /></>)
    await waitFor(() => expect(screen.getByRole('link', { name: 'ACME-7' })).toHaveAttribute('href', 'https://linear.app/acme/issue/ACME-7'))
  })

  it('records a pitch answer, removes it from NEEDS YOU, then sends Commander an FYI', async () => {
    const user = userEvent.setup()
    render(
      <>
        <Navigator />
        <InspectorColumn />
        <AskDock role="commander" />
      </>,
    )
    await user.click(screen.getByRole('tab', { name: /Needs you/ }))
    const queue = screen.getByRole('listbox')
    await user.click(within(queue).getAllByRole('option')[0])
    expect(useStore.getState().selected).toEqual({ kind: 'ask', key: 'ask:example-app:example-app-t03.2' })

    // The pitch is the panel's tab; its choices sit above Commander's input bar. There is no Graph tab.
    // The ask's workstream is in focus: the top bar names it.
    await waitFor(() => expect(useStore.getState().focus?.title).toBeTruthy())
    expect(screen.queryByRole('tab', { name: /Graph/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: /Ask the mirror admin/ }))

    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/api/chat/commander/send'))).toBe(true))
    const sent = calls.find((c) => c.url.endsWith('/api/chat/commander/send'))!.body as { text: string }
    expect(sent.text.split('\n')[0]).toBe('FYI: Answer already recorded: Ask the mirror admin to release catalog-parser 2.4.1')
    const resolved = calls.find((c) => c.url.endsWith('/api/cmd'))!
    expect((resolved.body as { argv: string[] }).argv).toEqual(expect.arrayContaining([
      'internal-resolve-needs', '--project', 'example-app', '--bead', 'example-app-t03.2',
      '--action', 'decide', '--choice', 'Ask the mirror admin to release catalog-parser 2.4.1', '--text',
    ]))
    expect(calls.indexOf(resolved)).toBeLessThan(calls.findIndex((c) => c.url.endsWith('/api/chat/commander/send')))
    expect(useStore.getState().snapshot!.needs_you.some((n) => n.bead_id === 'example-app-t03.2')).toBe(false)
    expect(screen.getByRole('tab', { name: /^Needs you\s*2$/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Ask the mirror admin/ })).toBeNull()
    // Recording the answer does not select another ask on the user's behalf.
    expect(useStore.getState().selected).toEqual({ kind: 'ask', key: 'ask:example-app:example-app-t03.2' })
  })

  it('a workstream click only selects it: no steps, gates or "no gates" in the sidebar', async () => {
    const user = userEvent.setup()
    const { container } = render(<Navigator />)
    const row = container.querySelector('.ws .row') as HTMLElement
    await user.click(row)
    expect(useStore.getState().selected.kind).toBe('ws')
    expect(container.querySelector('.ws-gates, .ws-stage, .ws-nogates')).toBeNull()
    expect(container.textContent).not.toMatch(/no gates|no workflow gate/i)
    expect(container.textContent).not.toContain('TEAM-101')
    expect(row.parentElement!.querySelectorAll('.row')).toHaveLength(1)
  })

  it('binds the inspector to the selected workstream', async () => {
    useStore.getState().goWorkstream('example-app:example-app-t01')
    render(<InspectorColumn />)
    await waitFor(() => expect(calls.some((c) => c.url.includes('/api/tabs?ws=example-app-t01&project=example-app'))).toBe(true))
    expect(screen.queryByRole('tab', { name: /Decision:/ })).toBeNull()
  })

  it('shows the quiet loader, never an error card, while cmdrd has no chat engine', async () => {
    render(<ChatSlot />)
    expect(await screen.findByLabelText('Loading chat')).toBeInTheDocument()
    expect(screen.queryByRole('heading')).toBeNull()
  })
})
