// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, beforeEach, afterEach, beforeAll, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ChatPane } from '../ChatPane'
import { ReplayTransport, type ConnState } from '../transport'
import { FIXTURES, manyEvents } from '../fixtures'
import { parseRecording } from '../adapter'
import { sample } from './samples'
import type { ChatEvent } from '../events'

const RECT = { width: 900, height: 4000 }

function mount(events: ChatEvent[], props: Partial<React.ComponentProps<typeof ChatPane>> = {}) {
  const transport = new ReplayTransport(events, { echo: true })
  const utils = render(<ChatPane role={props.role ?? 'op-test'} transport={transport} initialRect={RECT} {...props} />)
  return { transport, ...utils }
}

// jsdom has no layout: give the scroll container a size so the virtualizer
// renders rows.
beforeAll(() => {
  const size = (dim: 'clientWidth' | 'clientHeight', v: number) => {
    const orig = Object.getOwnPropertyDescriptor(Element.prototype, dim)!
    Object.defineProperty(HTMLElement.prototype, dim, {
      configurable: true,
      get(this: HTMLElement) {
        return this.classList.contains('cc-list') ? v : orig.get!.call(this)
      },
    })
  }
  size('clientWidth', RECT.width)
  size('clientHeight', RECT.height)
  // Rows measure as 60px (jsdom would say 0, which makes every row "fit").
  const oh = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')!
  Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
    configurable: true,
    get(this: HTMLElement) {
      return this.classList.contains('cc-vrow') ? 60 : oh.get!.call(this)
    },
  })
})

beforeEach(() => {
  localStorage.clear()
})

afterEach(() => cleanup())

describe('rendering', () => {
  it.each(Object.keys(FIXTURES))('renders the %s fixture without crashing', async (name) => {
    mount(FIXTURES[name as keyof typeof FIXTURES]())
    await waitFor(() => expect(screen.queryByText('No messages yet.')).toBeNull())
  })

  it('renders a synthetic stream: bubbles, mail row, tool steps in an activity row', async () => {
    const user = userEvent.setup()
    mount(parseRecording(sample('s1')))
    await screen.findByText('Give six lines about a sample library.')
    expect(screen.getAllByText(/SAMPLE-17/).length).toBeGreaterThan(0)
    for (const away of screen.queryAllByRole('button', { name: /While you were away/ })) await user.click(away)
    expect(screen.getAllByText(/^Message from/).length).toBeGreaterThan(0)
    const row = screen.getAllByRole('button', { name: /Worked/ })[0]
    await user.click(row)
    expect(document.querySelectorAll('.ct-step').length).toBeGreaterThan(0)
  })

  it('renders markdown code blocks with a copy button and tables', async () => {
    mount(FIXTURES.longAnswer())
    await screen.findByText(/func \(s \*Session\) pump/)
    expect(screen.getAllByRole('button', { name: 'Copy' }).length).toBeGreaterThan(0)
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: /stream-json\s*notes/ })).toHaveAttribute('target', '_blank')
  })

  it('shows a sub-agent step with nested child tool calls', async () => {
    const user = userEvent.setup()
    mount(FIXTURES.subAgent())
    await user.click((await screen.findAllByRole('button', { name: /helper/i }))[0])
    const steps = document.querySelector('.ct-steps') as HTMLElement
    await user.click(within(steps).getByRole('button', { name: /helper/ }))
    const detail = document.querySelector('.ct-step-detail') as HTMLElement
    expect(within(detail).getByText('Explore')).toBeInTheDocument()
    expect(detail.querySelectorAll('.cc-children [data-tool]').length).toBe(4)
  })

  it('handles a 10k-event replay', async () => {
    const t0 = performance.now()
    mount(manyEvents(10_000))
    // Replies wrap each word in a span, so match the paragraph's whole text.
    await screen.findAllByText((_, el) => el?.tagName === 'P' && /Answer \d+: done/.test(el.textContent ?? ''))
    expect(performance.now() - t0).toBeLessThan(20_000)
    // Only the latest exchanges render until you ask for earlier ones.
    expect(document.querySelectorAll('[data-pair]').length).toBeLessThanOrEqual(40)
    expect(screen.getByRole('button', { name: /Show earlier messages/ })).toBeInTheDocument()
  })
})

describe('approvals', () => {
  it('allow once sends updatedInput and resolves the card', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.permissionPrompt())
    await user.click(await screen.findByRole('button', { name: /Allow once/ }))
    expect(transport.calls.at(-1)).toMatchObject({ verb: 'approve', req: { id: 'p2', behavior: 'allow', updatedInput: { command: expect.stringContaining('go clean') } } })
    expect((transport.calls.at(-1) as { req: { updatedPermissions?: unknown } }).req.updatedPermissions).toBeUndefined()
    await waitFor(() => expect(screen.queryByRole('button', { name: /Allow once/ })).toBeNull())
  })

  it('always allow applies the narrow rule suggestion', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.permissionPrompt())
    await user.click(await screen.findByRole('button', { name: /Always allow/ }))
    expect(transport.calls.at(-1)).toMatchObject({
      verb: 'approve',
      req: { behavior: 'allow', updatedPermissions: [{ type: 'addRules', rules: [{ toolName: 'Bash', ruleContent: 'go clean:*' }] }] },
    })
  })

  it('keyboard: 3 opens the deny reason, Enter sends it', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.permissionPrompt())
    const card = await screen.findByRole('group', { name: /Permission request for Bash/ })
    card.focus()
    await user.keyboard('3')
    const reason = await screen.findByPlaceholderText(/Why\?/)
    await user.type(reason, 'not on main{Enter}')
    expect(transport.calls.at(-1)).toMatchObject({ verb: 'approve', req: { id: 'p2', behavior: 'deny', message: 'not on main' } })
  })

  it('keyboard: 1 allows once', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.permissionPrompt())
    const card = await screen.findByRole('group', { name: /Permission request for Bash/ })
    card.focus()
    await user.keyboard('1')
    expect(transport.calls.at(-1)).toMatchObject({ verb: 'approve', req: { behavior: 'allow' } })
  })

  it('AskUserQuestion: pick an option and submit answers', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.questionAndPlan())
    const submit = await screen.findByRole('button', { name: 'Submit answer' })
    expect(submit).toBeDisabled()
    await user.click(screen.getByText('Stop the TUI, resume headless'))
    await user.click(submit)
    expect(transport.calls.at(-1)).toEqual({
      verb: 'answer',
      role: 'op-test',
      req: { id: 'rq1', answers: { 'How should a Manager move from tmux to native chat?': 'Stop the TUI, resume headless' } },
    })
  })

  it('AskUserQuestion: free-text "Other" answer', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.questionAndPlan())
    await user.type(await screen.findByLabelText('Other answer'), 'Ask me later{Enter}')
    expect(transport.calls.at(-1)).toMatchObject({ verb: 'answer', req: { answers: { 'How should a Manager move from tmux to native chat?': 'Ask me later' } } })
  })

  it('AskUserQuestion: multi-select joins answers', async () => {
    const user = userEvent.setup()
    const { transport } = mount([])
    act(() =>
      transport.emit({
        kind: 'question',
        id: 'q9',
        questions: [{ question: 'Which tiers?', multiSelect: true, options: [{ label: 'Operators' }, { label: 'Managers' }, { label: 'Commander' }] }],
      }),
    )
    await user.click(await screen.findByText('Operators'))
    await user.click(screen.getByText('Commander'))
    await user.click(screen.getByRole('button', { name: 'Submit answer' }))
    expect(transport.calls.at(-1)).toMatchObject({ req: { answers: { 'Which tiers?': 'Operators, Commander' } } })
  })

  it('ExitPlanMode: approve and keep planning', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.planOnly())
    await screen.findByText('Manager cutover')
    await user.click(screen.getByRole('button', { name: 'Keep planning' }))
    await user.type(screen.getByPlaceholderText(/What should change/), 'add a rollback step{Enter}')
    expect(transport.calls.at(-1)).toMatchObject({ verb: 'approve', req: { id: 'rp1', behavior: 'deny', message: 'add a rollback step' } })

    const t2 = mount(FIXTURES.planOnly(), { role: 'op-2' })
    await user.click(await within(t2.container).findByRole('button', { name: 'Approve plan' }))
    expect(t2.transport.calls.at(-1)).toMatchObject({ verb: 'approve', req: { id: 'rp1', behavior: 'allow', updatedInput: { plan: expect.stringContaining('Manager cutover') } } })
  })
})

describe('composer', () => {
  it('Enter sends, Shift+Enter makes a newline', async () => {
    const user = userEvent.setup()
    const { transport } = mount([])
    const box = screen.getByRole('textbox', { name: 'Message' })
    await user.type(box, 'line one{Shift>}{Enter}{/Shift}line two{Enter}')
    expect(transport.calls).toEqual([{ verb: 'send', role: 'op-test', req: expect.objectContaining({ text: 'line one\nline two', now: false }) }])
    expect(box).toHaveValue('')
    await screen.findByText(/line one/)
  })

  it('shows Stop while a turn runs, sends interrupt, and holds a follow-up inside the composer until it is read', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.toolHeavy())
    await waitFor(() => expect(document.querySelector('.ct-activity-head .ct-shimmer, .ct-away-working')).not.toBeNull()) // the live step, or the away row working
    await user.click(await screen.findByRole('button', { name: /Stop/ }))
    expect(transport.calls.at(-1)).toEqual({ verb: 'interrupt', role: 'op-test' })
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'also check NEEDS YOU{Enter}')
    const waiting = await screen.findByRole('list', { name: 'Sent, not read yet' })
    expect(within(waiting).getByText('also check NEEDS YOU')).toBeInTheDocument()
    expect(waiting.closest('.cc-composer')).not.toBeNull()
    const jump = screen.getByRole('button', { name: 'Jump to latest' })
    expect(jump.closest('.ct-root')).not.toBeNull()
    expect(jump.closest('.ct-viewport')).toBeNull()
    expect(document.querySelector('.ct-bubble')?.textContent).not.toBe('also check NEEDS YOU')
  })

  it('Mod+Enter sends now while running', async () => {
    const user = userEvent.setup()
    const { transport } = mount(FIXTURES.toolHeavy())
    await screen.findByRole('button', { name: /Stop/ })
    const box = screen.getByRole('textbox', { name: 'Message' })
    await user.type(box, 'urgent')
    await user.keyboard('{Control>}{Enter}{/Control}')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    const sends = transport.calls.filter((c) => c.verb === 'send')
    expect(sends).toHaveLength(1)
    expect(sends[0]).toMatchObject({ req: { text: 'urgent', now: true } })
  })

  it('slash-command autocomplete completes with Tab', async () => {
    const user = userEvent.setup()
    mount(FIXTURES.longAnswer())
    const box = screen.getByRole('textbox', { name: 'Message' })
    await user.type(box, '/comp')
    const list = await screen.findByRole('listbox', { name: 'Slash commands' })
    expect(within(list).getByText('/compact')).toBeInTheDocument()
    await user.keyboard('{Tab}')
    expect(box).toHaveValue('/compact ')
    expect(screen.queryByRole('listbox')).toBeNull()
  })

  it('persists drafts per role', async () => {
    const user = userEvent.setup()
    const first = mount([], { role: 'op-a' })
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'half-written')
    await waitFor(() => expect(localStorage.getItem('cmdr.chat.draft.op-a')).toBe('half-written'))
    first.unmount()
    mount([], { role: 'op-a' })
    expect(screen.getByRole('textbox', { name: 'Message' })).toHaveValue('half-written')
  })

  it('readonly shows the owner banner instead of an input', async () => {
    mount(FIXTURES.longAnswer(), { readonly: 'Owned by a terminal' })
    expect(screen.queryByRole('textbox', { name: 'Message' })).toBeNull()
    expect(screen.getByText('Owned by a terminal')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Open terminal' })).toBeNull()
  })

  it('header menu holds the session stats and no pop-out', async () => {
    const user = userEvent.setup()
    mount(FIXTURES.longAnswer())
    expect(screen.queryByText('Session cost')).toBeNull() // stats stay out of the header until asked for
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    expect(screen.getByText('Session cost')).toBeInTheDocument()
    expect(screen.queryByRole('menuitem', { name: /Pop out/ })).toBeNull()
  })

  it('shows the actual Codex model in the menu without a provider badge', async () => {
    const user = userEvent.setup()
    mount([{ kind: 'init', model: 'codex:gpt-5.5', seq: 1, session: 's', ts: Date.now() } as ChatEvent])
    await screen.findByTitle(/gpt-5.5/)
    expect(screen.queryByText('Codex')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Chat menu' }))
    expect(screen.getByText('gpt-5.5')).toBeInTheDocument()
  })

  it('a Claude chat has no runtime badge', async () => {
    mount(FIXTURES.longAnswer())
    await screen.findByTitle(/Opus 5.5/)
    expect(screen.queryByText('Codex')).toBeNull()
  })

  it('a terminal session: badge + Move to app, composer sends, and a mail delivery shows as sent', async () => {
    const user = userEvent.setup()
    let moved = 0
    const transport = new ReplayTransport(FIXTURES.longAnswer())
    transport.send = async (role, req) => {
      transport.calls.push({ verb: 'send', role, req })
      return { delivered: 'mail' }
    }
    render(<ChatPane role="commander" title="Commander" transport={transport} initialRect={RECT} terminal={{ label: 'terminal session', onMove: () => moved++ }} />)
    expect(screen.getByText('terminal session')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Stop/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Move to app' }))
    expect(moved).toBe(1)
    const box = screen.getByRole('textbox', { name: 'Message' })
    expect(box).toHaveAttribute('placeholder', expect.stringContaining('delivered by mail'))
    await user.type(box, 'make the sidebar wider{Enter}')
    expect(await screen.findByText('delivered by mail to commander')).toBeInTheDocument()
    expect(screen.getByText('make the sidebar wider').closest('.ct-user')).not.toHaveClass('pending')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('never shows the raw "no running process" error', async () => {
    const user = userEvent.setup()
    const transport = new ReplayTransport([])
    transport.send = async () => {
      throw new Error('chat: no running process for manager-x')
    }
    render(<ChatPane role="manager-x" transport={transport} initialRect={RECT} />)
    await user.type(screen.getByRole('textbox', { name: 'Message' }), 'hi{Enter}')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Not sent: manager-x has no running chat to deliver to.')
    expect(alert).not.toHaveTextContent('no running process')
  })
})

describe('reconnecting', () => {
  afterEach(() => vi.useRealTimers())

  it('shows a quiet chip, keeps sends in order, and raises the error banner only after 15s', async () => {
    let setConn: (s: ConnState) => void = () => {}
    class Flaky extends ReplayTransport {
      subscribe(role: string, since: number, onEvent: (e: ChatEvent[]) => void, onConn?: (s: ConnState) => void) {
        const unsub = super.subscribe(role, since, onEvent, onConn)
        setConn = onConn ?? setConn
        return unsub
      }
    }
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const transport = new Flaky([], { echo: true })
    render(<ChatPane role="op-test" transport={transport} initialRect={RECT} />)
    act(() => setConn('closed'))
    expect(document.querySelector('.cc-reconnecting')).toHaveTextContent('reconnecting…')
    expect(screen.queryByRole('alert')).toBeNull()
    const box = screen.getByRole('textbox', { name: 'Message' })
    for (const t of ['first', 'second']) {
      fireEvent.change(box, { target: { value: t } })
      fireEvent.keyDown(box, { key: 'Enter' })
    }
    expect(transport.calls).toHaveLength(0)
    act(() => vi.advanceTimersByTime(16_000))
    expect(screen.getByRole('alert')).toHaveTextContent(/Lost connection/)
    act(() => setConn('open'))
    await waitFor(() => expect(transport.calls.filter((c) => c.verb === 'send')).toHaveLength(2))
    expect(transport.calls.map((c) => (c as { req: { text: string } }).req.text)).toEqual(['first', 'second'])
    expect(screen.queryByRole('alert')).toBeNull()
    expect(document.querySelector('.cc-reconnecting')).toBeNull()
  })

  it('runs the thread down to the input bar, floating the rest over it', () => {
    let measure = () => {}
    vi.stubGlobal('ResizeObserver', class {
      constructor(cb: () => void) { measure = cb }
      observe() {}
      disconnect() {}
    })
    render(<ChatPane role="op-test" transport={new ReplayTransport([], { echo: true })} initialRect={RECT} />)
    const stage = document.querySelector<HTMLElement>('.cc-stage')!
    const floats = stage.querySelector<HTMLElement>(':scope > .cc-floats')!
    expect(stage.querySelector('.cc-composer')).toBeNull()
    expect(stage.nextElementSibling).toHaveClass('cc-composer-wrap')
    vi.spyOn(floats, 'offsetHeight', 'get').mockReturnValue(120)
    act(() => measure())
    expect(stage.style.getPropertyValue('--cc-floats-h')).toBe('120px')
    vi.unstubAllGlobals()
  })
})
