// Native chat attach: selecting a workstream connects the pane to its Manager's
// live chat (WS replay from since=0 renders the hydrated history); a chat that
// is not running starts on its own unless a terminal holds it; then Move to
// app needs a confirm. Pop out is confirmed too. Agents, accounts and usage live in Settings (the gear).
import { act, cleanup, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { useStore } from '../src/renderer/src/store'
import { ChatSlot, useChatTarget } from '../src/renderer/src/components/ChatSlot'
import { refreshChats, type ChatAccount } from '../src/renderer/src/chatApi'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { Rail } from '../src/renderer/src/components/Rail'
import { managerConversation } from '../src/features/chat/engineFixtures'
import { operatorLabel, operatorRoles } from '../src/renderer/src/viewmodel'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
const cfg = { apiUrl: 'http://127.0.0.1:9', token: 't' }
const OP = 'operator-op-exp-3-sample-example-app-t06-1-1'

// openManager opens a workstream's Manager chat (a workstream click keeps Commander's).
const openManager = (bead: string) => useStore.getState().select({ kind: 'agent', key: `manager-${bead}`, ws: `example-app:${bead}` })


type Call = { url: string; method: string; body?: unknown }
let calls: Call[]
let sessions: Record<string, unknown>[]
let postReply: (url: string) => [number, unknown]
let accounts: unknown[]

// account is a /api/chat accounts entry; usage "ok" reads 5h 30% · 7d 10%.
const account = (name: string, loggedIn = true, usage?: 'ok' | 'out'): ChatAccount => ({
  name,
  runtime: name === 'codex' ? 'codex' : 'claude',
  logged_in: loggedIn,
  email: `${name}@example.test`,
  usage:
    name === 'second'
      ? { runtime: 'claude' as const, account: name, status: 'warning' as const, five_hour: { utilization: 0.3 }, seven_day: { utilization: 0.1 }, role: 'commander', at: '2026-10-22T15:00:00Z' }
      : usage === 'out'
        ? { runtime: 'claude' as const, account: name, status: 'out' as const, five_hour: { utilization: 1 }, role: 'commander', at: '2026-10-22T15:00:00Z' }
        : undefined,
})

class FakeWS {
  static all: FakeWS[] = []
  onopen?: () => void
  onmessage?: (m: { data: string }) => void
  onclose?: () => void
  onerror?: () => void
  constructor(public url: string) {
    FakeWS.all.push(this)
    const role = decodeURIComponent(/\/api\/chat\/([^/]+)\/events/.exec(url)![1])
    queueMicrotask(() => {
      this.onopen?.()
      this.onmessage?.({ data: JSON.stringify(managerConversation(role)) })
    })
  }
  close() {}
}

function fakeFetch() {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    calls.push({ url, method, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.endsWith('/api/chat')) return json(200, { ok: true, sessions, accounts })
    if (url.endsWith('/api/settings')) return json(200, { ok: true, settings: { user_name: 'Test User', codex_manager_share: 0.25, codex_operator_share: 0.5 } })
    if (url.endsWith('/api/usage/agents'))
      return json(200, {
        ok: true,
        accounts: [
          {
            key: 'codex',
            today_tokens: 1_200_000,
            week_tokens: 96_600_000,
            week_usd: 0,
            agents: [{ account: 'codex', runtime: 'codex', role: 'manager-example-app-t03', model: 'gpt-6-astra', today_tokens: 1_200_000, week_tokens: 96_600_000, week_usd: 0, week_turns: 333 }],
          },
        ],
      })
    if (url.endsWith('/api/chat/accounts')) return json(200, { ok: true, account: 'third', opened: true })
    if (method === 'POST') return json(...postReply(url))
    if (url.includes('/api/tabs')) return json(200, { project_id: 1, ws: '', active_id: 0, tabs: [] })
    return json(404, {})
  })
}

const status = (role: string, running: boolean, extra = {}) => ({ role, running, state: 'idle', last_seq: running ? 19 : 0, total_cost_usd: 3.6, pending: [], ...extra })
const posts = () => calls.filter((c) => c.method === 'POST')

beforeEach(() => {
  calls = []
  FakeWS.all = []
  sessions = [status('manager-example-app-t03', true), status('manager-example-app-t01', false), status(OP, true, { state: 'working' })]
  postReply = () => [200, { ok: true }]
  accounts = []
  vi.stubGlobal('fetch', fakeFetch())
  vi.stubGlobal('WebSocket', FakeWS)
  useStore.setState({ snapshot: snap, config: { ...cfg }, selected: { kind: 'commander' }, cursor: 'commander', chatEngine: 'checking', chats: {}, accounts: [], providers: {} })
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('native chat attach', () => {
  it('a workstream click keeps the chat on Commander; its Manager opens only when asked for', () => {
    useStore.getState().clickWorkstream('example-app:example-app-t03')
    expect(renderHook(() => useChatTarget()).result.current.role).toBe('commander')
    act(() => openManager('example-app-t03'))
    expect(renderHook(() => useChatTarget()).result.current).toMatchObject({ role: 'manager-example-app-t03', kind: 'manager' })
  })

  it('attaches the selected workstream to its Manager chat and renders the replayed history', async () => {
    openManager('example-app-t03')
    render(<ChatSlot />)
    // jsdom has no layout, so the virtual list draws no rows: the header proves
    // the replay landed (model from the session event, cost from turn_result);
    // engine.test.ts checks the history items themselves.
    expect(await screen.findByTitle(/Sonnet 5.5/)).toBeInTheDocument()
    expect(FakeWS.all[0].url).toBe('ws://127.0.0.1:9/api/chat/manager-example-app-t03/events?since=0&token=t')
    expect(screen.getByTitle(/\$3\.60/)).toBeInTheDocument()
    expect(screen.getByRole('textbox')).toBeInTheDocument()
    expect(posts()).toEqual([]) // attaching never POSTs
  })

  it('starts a stopped chat on its own, and offers Move to app (confirmed) when a terminal holds it', async () => {
    const user = userEvent.setup()
    openManager('example-app-t01')
    postReply = (url) => (url.endsWith('/start') ? [409, { ok: false, error: 'owned by terminal', owned_by: 'terminal', pid: 4242 }] : [200, { ok: true }])
    render(<ChatSlot />)
    expect(await screen.findByText(/terminal session/)).toBeInTheDocument()
    expect(posts().map((c) => c.url)).toEqual(['http://127.0.0.1:9/api/chat/manager-example-app-t01/start'])

    await user.click(await screen.findByRole('button', { name: 'Move to app' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('pid 4242')
    expect(dialog).toHaveTextContent('same session')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(posts()).toHaveLength(1) // cancel sends nothing

    await user.click(await screen.findByRole('button', { name: 'Move to app' }))
    sessions = [status('manager-example-app-t01', true)]
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Move to app' }))
    await waitFor(() => expect(posts().map((c) => c.url.split('/').pop())).toEqual(['start', 'handover']))
    expect(posts()[1].body).toEqual({ confirm: true })
    expect(await screen.findByTitle(/Sonnet 5.5/)).toBeInTheDocument()
  })

  it('never auto-starts a chat cmdrd reports as held by a terminal', async () => {
    sessions = [status('manager-example-app-t01', false, { owned_by: 'terminal', owner_pid: 77 })]
    openManager('example-app-t01')
    render(<ChatSlot />)
    expect(await screen.findByText(/terminal session/)).toBeInTheDocument()
    expect(posts()).toEqual([])
  })

  it('retries once when a late Commander session appears, then sends directly', async () => {
    const scenario = JSON.parse(readFileSync(join(__dirname, '../../tests/evals/scenarios/late-commander-session.json'), 'utf8'))
    sessions = [status(scenario.role, false)]
    postReply = () => [500, { error: scenario.missing_error }]
    render(<ChatSlot />)
    expect(await screen.findByRole('alert')).toHaveTextContent(scenario.missing_error)
    expect(posts()).toHaveLength(1)
    await act(async () => { await refreshChats(); await refreshChats() })
    expect(posts()).toHaveLength(1)

    sessions = [status(scenario.role, false, { session_id: 'sample-session' })]
    postReply = () => {
      sessions = [status(scenario.role, true, { session_id: 'sample-session' })]
      return [200, { ok: true }]
    }
    await act(async () => { await refreshChats() })
    await screen.findByRole('textbox')
    expect(posts().map((c) => c.url.split('/').pop())).toEqual(['start', 'start'])
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByText(/terminal session/)).toBeNull()
    const user = userEvent.setup()
    await user.type(screen.getByRole('textbox'), scenario.message)
    await user.keyboard('{Enter}')
    await waitFor(() => expect(posts().some((c) => c.url.endsWith('/send'))).toBe(true))
    expect(posts().filter((c) => c.url.endsWith('/start'))).toHaveLength(2)
  })

  it('keeps a failed late-session start bounded across repeated polls', async () => {
    sessions = [status('commander', false)]
    postReply = () => [500, { error: 'start unavailable' }]
    render(<ChatSlot />)
    await screen.findByRole('alert')
    sessions = [status('commander', false, { session_id: 'sample-session' })]
    await act(async () => { await refreshChats() })
    await waitFor(() => expect(posts()).toHaveLength(2))
    await screen.findByRole('alert')
    await act(async () => { await refreshChats(); await refreshChats() })
    expect(posts()).toHaveLength(2)
  })

  it.each(['terminal', 'headless'])('preserves %s ownership when a late session appears', async (owner) => {
    sessions = [status('commander', false)]
    postReply = () => [500, { error: 'no session yet' }]
    render(<ChatSlot />)
    await screen.findByRole('alert')
    sessions = [status('commander', false, { session_id: 'held-session', owned_by: owner, owner_pid: 77 })]
    await act(async () => { await refreshChats() })
    expect(await screen.findByText(owner === 'terminal' ? /terminal session/ : /headless process/)).toBeInTheDocument()
    expect(posts()).toHaveLength(1)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('a running chat has no pop-out to terminal in its menu', async () => {
    const user = userEvent.setup()
    openManager('example-app-t03')
    render(<ChatSlot />)
    await screen.findByTitle(/Sonnet 5.5/)
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    expect(screen.queryByRole('menuitem', { name: /Pop out/ })).toBeNull()
    expect(posts()).toEqual([])
  })

  it('switches a running chat between Claude accounts and Codex from its menu', async () => {
    const user = userEvent.setup()
    accounts = [account('default'), account('second'), account('spare', false), account('codex')]
    openManager('example-app-t03')
    render(<ChatSlot />)
    await screen.findByTitle(/Sonnet 5.5/)
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    // One "Switch to" list: each logged-in Claude account (the current one marked), then Codex.
    const items = screen.getAllByRole('menuitemradio')
    expect(items.map((i) => i.textContent)).toEqual(['Claude · defaultno usage reported yetcurrent', 'Claude · second5h 30% · 7d 10%', 'Codexno usage reported yet', 'Start freshnew context, reads this chat first'])
    expect(items[0]).toHaveAttribute('aria-checked', 'true')
    await user.click(screen.getByRole('menuitemradio', { name: /Codex/ }))
    await waitFor(() => expect(posts().map((c) => [c.url, c.body])).toEqual([['http://127.0.0.1:9/api/chat/manager-example-app-t03/runtime', { runtime: 'codex' }]]))

    sessions = [status('manager-example-app-t03', true, { runtime: 'codex' })]
    await act(() => refreshChats())
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    await user.click(await screen.findByRole('menuitemradio', { name: /Claude · second/ }))
    await waitFor(() => expect(posts()[1]).toMatchObject({ url: 'http://127.0.0.1:9/api/chat/manager-example-app-t03/account', body: { account: 'second' } }))
  })

  it('shows why a switch was refused', async () => {
    const user = userEvent.setup()
    openManager('example-app-t03')
    postReply = () => [409, { ok: false, error: 'chat: a turn is in progress: wait for the turn to end before switching runtime' }]
    render(<ChatSlot />)
    await screen.findByTitle(/Sonnet 5.5/)
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    await user.click(screen.getByRole('menuitemradio', { name: /Codex/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('wait for the turn to end')
  })

  it('a chat stopped at a usage limit offers the other accounts with their usage; one click switches and continues', async () => {
    const user = userEvent.setup()
    accounts = [account('default', true, 'out'), account('second'), account('codex')]
    sessions = [status('manager-example-app-t03', true, { account: 'default', usage_limit: { text: "You've hit your limit · resets 4pm", runtime: 'claude', account: 'default', at: '' } })]
    openManager('example-app-t03')
    render(<ChatSlot />)
    const notice = await screen.findByRole('alert', { name: 'Usage limit' })
    expect(notice).toHaveTextContent('Claude · default is out of usage')
    expect(notice).toHaveTextContent("You've hit your limit · resets 4pm")
    expect(within(notice).getByRole('button', { name: /Claude · second/ })).toHaveTextContent('5h 30% · 7d 10%')
    await user.click(within(notice).getByRole('button', { name: /Claude · second/ }))
    await waitFor(() => expect(posts()[0]).toMatchObject({ url: 'http://127.0.0.1:9/api/chat/manager-example-app-t03/account', body: { account: 'second', continue: true } }))
  })

  it('the sidebar is the work; the rail is Home, the Settings gear and the user', async () => {
    const user = userEvent.setup()
    useStore.setState({
      chatEngine: 'on',
      chats: { [OP]: status(OP, true, { state: 'working', runtime: 'codex' }) as never },
      providers: { [OP]: { runtime: 'codex' } },
      accounts: [account('default'), account('codex')],
    })
    const { container } = render(<><Navigator /><Rail /></>)
    await screen.findByRole('tab', { name: 'All' })
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+$/, ''))).toEqual(['Needs you', 'All'])
    expect(await screen.findByTitle('Test User')).toHaveTextContent('TU')
    expect([...container.querySelectorAll('.rail button')].map((b) => b.getAttribute('aria-label'))).toEqual(['Home', 'Progress', 'Settings'])
    await user.click(screen.getByRole('button', { name: 'Settings' }))
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    expect(within(dialog).getAllByRole('button', { name: /Connections|Agents|Display/ }).map((b) => b.textContent)).toEqual(['Connections', 'Agents', 'Display'])
    const operators = (await within(dialog).findByRole('rowheader', { name: 'Operators' })).closest('tr') as HTMLElement
    const [claude, codex, all] = within(operators).getAllByRole('cell')
    expect(claude).toHaveTextContent('—')
    expect(codex).toHaveTextContent('1 working · 0 idle')
    expect(all).toHaveTextContent('1 working · 0 idle')
  })

  it('Settings, Agents, Usage: one row per account with its windows; Add account creates it', async () => {
    const user = userEvent.setup()
    useStore.setState({ chatEngine: 'on', accounts: [account('default', true, 'out'), account('second'), account('spare', false), account('codex')] })
    render(<Rail />)
    await user.click(await screen.findByRole('button', { name: 'Settings' }))
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    await user.click(within(dialog).getByRole('tab', { name: 'Usage' }))
    const second = within(dialog).getByLabelText('Claude: second@example.test')
    expect(second).toHaveTextContent('running low')
    expect(within(second).getByRole('meter', { name: '5 hours' })).toHaveAttribute('aria-valuenow', '30')
    expect(within(dialog).getByLabelText('Claude: default@example.test')).toHaveTextContent('out')
    // A signed-out profile cmdrd still knows the identity of stays listed to reconnect.
    const spare = within(dialog).getByLabelText('Claude: spare@example.test')
    expect(spare).toHaveTextContent('Claude, not connected')
    expect(within(spare).getByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Codex: codex@example.test')).toBeInTheDocument()
    // Who used each account's tokens, and on which model.
    const top = await within(within(dialog).getByLabelText('Codex: codex@example.test')).findByLabelText('Top agents')
    expect(top).toHaveTextContent('1.2M tokens today, 97M this week')
    expect(top.querySelector('.acct-agent')).toHaveTextContent('gpt-6-astra97M')
    await user.click(within(dialog).getByRole('button', { name: 'Add account' }))
    await user.type(within(dialog).getByRole('textbox', { name: 'Account name' }), 'Third')
    await user.click(within(dialog).getByRole('button', { name: 'Add account' }))
    await waitFor(() => expect(posts().at(-1)).toMatchObject({ url: 'http://127.0.0.1:9/api/chat/accounts', body: { name: 'third' } }))
    expect(await within(dialog).findByText(/Account added/)).toBeInTheDocument()
  })

  it('Settings, Agents: the account order of each kind saves to cmdrd', async () => {
    const user = userEvent.setup()
    useStore.setState({ chatEngine: 'on', accounts: [account('default'), account('work'), account('codex')] })
    render(<Rail />)
    await user.click(await screen.findByRole('button', { name: 'Settings' }))
    const dialog = screen.getByRole('dialog', { name: 'Settings' })
    const commander = await within(dialog).findByRole('group', { name: 'Commander accounts' })
    const agents = within(dialog).getByRole('group', { name: 'Agents accounts' })
    expect(within(commander).getAllByRole('listitem').map((li) => li.querySelector('span')?.textContent)).toEqual(['Any Claude account', 'Codex'])
    expect(within(agents).getAllByRole('listitem').map((li) => li.querySelector('span')?.textContent)).toEqual(['Codex', 'Any Claude account'])
    await user.click(within(commander).getByRole('button', { name: 'Move up: Codex' }))
    await waitFor(() => expect(posts().at(-1)).toMatchObject({ url: 'http://127.0.0.1:9/api/settings/agent-accounts', body: { kind: 'commander', accounts: ['codex', 'claude'] } }))
    await user.selectOptions(within(agents).getByRole('combobox', { name: 'Add a fallback' }), 'work')
    await waitFor(() => expect(posts().at(-1)).toMatchObject({ url: 'http://127.0.0.1:9/api/settings/agent-accounts', body: { kind: 'agent', accounts: ['codex', 'claude', 'work'] } }))
  })

  it('shows a tmux-only Operator read-only, with no actions', async () => {
    sessions = []
    act(() => useStore.getState().select({ kind: 'agent', key: OP, ws: 'example-app:example-app-t06' }))
    render(<ChatSlot />)
    expect(await screen.findByText(/runs in the tmux cockpit/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start chat' })).toBeNull()
  })
})

describe('operator roles', () => {
  it('collects operator owners of unfinished steps plus native roles named after the bead', () => {
    const t06 = snap.workstreams.find((w) => w.bead_id === 'example-app-t06')!
    expect(operatorRoles(t06)).toEqual([OP])
    expect(operatorRoles(t06, ['operator-op-new-example-app-t06-2-1', 'operator-op-x-example-app-t05-1-1', 'manager-example-app-t06'])).toEqual([OP, 'operator-op-new-example-app-t06-2-1'])
    expect(operatorLabel(OP, 'example-app-t06')).toBe('exp-3-sample · 1-1')
  })
})

describe('chat engine detection', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('keeps checking while cmdrd is unreachable; only a 404 means no engine', async () => {
    const { refreshChats } = await import('../src/renderer/src/chatApi')
    useStore.setState({ config: { apiUrl: 'http://127.0.0.1:9', token: 't' }, chatEngine: 'checking' })
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    await refreshChats()
    expect(useStore.getState().chatEngine).toBe('checking')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 404 })))
    await refreshChats()
    expect(useStore.getState().chatEngine).toBe('off')
  })
})
