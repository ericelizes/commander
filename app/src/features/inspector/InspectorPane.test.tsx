// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { InspectorClient } from './client'
import { openInPanel } from './connections'
import { InspectorPane } from './InspectorPane'
import type { Card, SyncMsg, TabInfo, TabList, WebviewBridge } from './types'

afterEach(cleanup)

const tab = (id: number, over: Partial<TabInfo> = {}): TabInfo => ({
  id,
  position: id,
  title: `t${id}`,
  kind: 'url',
  target: `https://example.com/${id}`,
  renderer: '',
  detected: 'web',
  view: 'web',
  refresh_secs: 60,
  added_by: 'user',
  created_at: 0,
  ...over,
})

const card = (over: Partial<Card> = {}): Card => ({
  Renderer: 'notion',
  Title: 'Spec digest',
  State: '',
  URL: '',
  Meta: null,
  Sections: null,
  Markdown: '## Goals\n\n- ship **it**',
  Preformatted: false,
  Attn: false,
  UpdatedAt: '',
  FetchedAt: new Date().toISOString(),
  Err: '',
  ...over,
})

function fakeClient(list: TabList, cards: Record<number, Card> = {}) {
  const calls: string[][] = []
  const client: InspectorClient = {
    listTabs: vi.fn(async () => list),
    card: vi.fn(async (id: number) => ({ tab_id: id, card: cards[id], stale: false, refreshing: false })),
    cmd: vi.fn(async (argv: string[]) => {
      calls.push(argv)
      return { ok: true, stdout: '', stderr: '', exit: 0 }
    }),
  }
  return { client, calls }
}

const api = { baseUrl: '', token: 't' }

describe('InspectorPane web mode (no Electron bridge)', () => {
  it('shows a web tab as a link card with Open in browser plus its digest', async () => {
    const notion = tab(1, { title: 'Spec', target: 'https://www.notion.so/acme/Spec', detected: 'notion' })
    const { client } = fakeClient({ project_id: 1, ws: 'w', active_id: 1, tabs: [notion] }, { 1: card() })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)

    render(<InspectorPane api={api} client={client} bridge={null} />)

    await screen.findByText('Goals') // the digest's markdown, rendered
    expect(screen.getByText('https://www.notion.so/acme/Spec')).toBeTruthy()
    expect(screen.queryByLabelText('Back')).toBeNull() // no webview toolbar
    fireEvent.click(screen.getByText('Open in browser'))
    expect(open).toHaveBeenCalledWith('https://www.notion.so/acme/Spec', '_blank', 'noopener,noreferrer')
  })

  it('does not fetch a card for a plain web page', async () => {
    const { client } = fakeClient({ project_id: 0, ws: '', active_id: 1, tabs: [tab(1)] })
    render(<InspectorPane api={api} client={client} bridge={null} />)
    await screen.findByText('Open in browser')
    expect(client.card).not.toHaveBeenCalled()
  })

  it('detects web mode from window.commanderHost', async () => {
    const { client } = fakeClient({ project_id: 0, ws: '', active_id: 1, tabs: [tab(1)] })
    render(<InspectorPane api={api} client={client} />)
    await screen.findByText('Open in browser')
  })
})

function fakeBridge() {
  const syncs: SyncMsg[] = []
  const bridge: WebviewBridge = {
    sync: (m) => syncs.push(m),
    nav: vi.fn(),
    openExternal: vi.fn(),
    signinStatus: async () => ({ notion: true }),
    onState: () => () => {},
    onShortcut: () => () => {},
  }
  return { bridge, syncs }
}

describe('openInPanel with no panel on screen (the Commander chat alone)', () => {
  afterEach(() => {
    delete (window as { commanderHost?: unknown }).commanderHost
    vi.restoreAllMocks()
  })

  it('opens the page in the system browser through the desktop bridge', () => {
    const { bridge } = fakeBridge()
    ;(window as { commanderHost?: unknown }).commanderHost = { webview: bridge }
    openInPanel('https://github.com/example-org/example-repo/pull/12')
    expect(bridge.openExternal).toHaveBeenCalledWith('https://github.com/example-org/example-repo/pull/12')
  })

  it('opens a new browser tab in the web build', () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    openInPanel('https://github.com/example-org/example-repo/pull/12')
    expect(open).toHaveBeenCalledWith('https://github.com/example-org/example-repo/pull/12', '_blank', 'noopener,noreferrer')
  })
})

describe('InspectorPane with the Electron bridge', () => {
  it('syncs web tabs, focuses through /api/cmd and hides views under menus', async () => {
    const tabs = [tab(1), tab(2), tab(3, { kind: 'cmd', view: 'card', target: 'go test ./...', title: 'tests' })]
    const { client, calls } = fakeClient({ project_id: 7, ws: 'w-1', active_id: 1, tabs }, { 3: card({ Markdown: 'PASS', Preformatted: true, Title: 'tests' }) })
    const { bridge, syncs } = fakeBridge()
    render(<InspectorPane api={api} client={client} bridge={bridge} window={{ ws: 'w-1', project: 'p' }} />)

    await waitFor(() => expect(syncs.length).toBeGreaterThan(0))
    const last = () => syncs[syncs.length - 1]
    expect(last().tabs.map((t) => t.id)).toEqual([1, 2])
    expect(last().activeId).toBe(1)

    fireEvent.click(screen.getByText('t2'))
    await waitFor(() => expect(calls).toContainEqual(['tab', 'focus', '#2', '--ws', 'w-1', '--project', 'p']))
    expect(last().activeId).toBe(2) // optimistic

    // The sidebar's Connections opens a site's login page here.
    openInPanel('https://linear.app/login', 'Sign in: Linear')
    await waitFor(() =>
      expect(calls).toContainEqual(['tab', 'add', '--url', 'https://linear.app/login', '--title', 'Sign in: Linear', '--focus', '--ws', 'w-1', '--project', 'p']),
    )
    expect(bridge.openExternal).not.toHaveBeenCalled() // the panel took it

    fireEvent.click(screen.getByText('tests'))
    await screen.findByText('PASS') // cmd output as a card
    expect(last().activeId).toBeNull()
  })

  it('adds a URL from the strip and closes tabs', async () => {
    const { client, calls } = fakeClient({ project_id: 0, ws: '', active_id: 1, tabs: [tab(1)] })
    const { bridge } = fakeBridge()
    render(<InspectorPane api={api} client={client} bridge={bridge} />)
    await screen.findByText('t1')

    fireEvent.click(screen.getByLabelText('New tab'))
    const input = screen.getByPlaceholderText('Enter a web address')
    fireEvent.change(input, { target: { value: 'not a url' } })
    fireEvent.submit(input)
    expect(calls).toEqual([])
    fireEvent.change(input, { target: { value: 'github.com/o/r/pull/4' } })
    await act(async () => {
      fireEvent.submit(input)
    })
    expect(calls).toContainEqual(['tab', 'add', '--url', 'https://github.com/o/r/pull/4', '--focus'])

    fireEvent.click(screen.getByLabelText('Close t1'))
    await waitFor(() => expect(calls).toContainEqual(['tab', 'rm', '#1']))
  })

  it('brings a tab opened from outside to the front over the pinned tab, but not after a close', async () => {
    let list = { window: 'w', tabs: [tab(1)], active_id: 1 } as unknown as TabList
    const client: InspectorClient = { listTabs: vi.fn(async () => list), card: vi.fn(), cmd: vi.fn() } as unknown as InspectorClient
    const onPinned = vi.fn()
    render(<InspectorPane api={api} client={client} bridge={null} pollMs={20} pinned={[{ id: 'graph', title: 'Graph', content: <div /> }]} activePinned="graph" onPinned={onPinned} />)
    await waitFor(() => expect(client.listTabs).toHaveBeenCalled())
    list = { ...list, tabs: [tab(1), tab(2)], active_id: 2 }
    await waitFor(() => expect(onPinned).toHaveBeenCalledWith(null))
    onPinned.mockClear()
    list = { ...list, tabs: [tab(1)], active_id: 1 }
    await new Promise((r) => setTimeout(r, 80))
    expect(onPinned).not.toHaveBeenCalled()
    // A Manager's tab lands quietly: the pinned tab stays.
    list = { ...list, tabs: [tab(1), tab(3, { added_by: 'manager-x' })], active_id: 3 }
    await new Promise((r) => setTimeout(r, 80))
    expect(onPinned).not.toHaveBeenCalled()
  })

  it('shows the empty state', async () => {
    const { client } = fakeClient({ project_id: 0, ws: '', active_id: 0, tabs: [] })
    render(<InspectorPane api={api} client={client} bridge={fakeBridge().bridge} />)
    await screen.findByText('Nothing open')
  })
})
