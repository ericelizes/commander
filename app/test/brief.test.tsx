// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { Rail } from '../src/renderer/src/components/Rail'
import { TopBar } from '../src/renderer/src/components/TopBar'
import { BriefView } from '../src/features/brief/BriefView'
import { useBrief, type BriefStatus } from '../src/features/brief/brief'
import { useStore } from '../src/renderer/src/store'
import { homeActive, isPage } from '../src/renderer/src/viewmodel'

const ready: BriefStatus = {
  generating: false,
  next_at: '2026-11-20T18:00:00Z',
  evidence: [
    { id: 'step:p-ws.2.1', workstream: 'p:p-ws', when: 'board', label: 'Now on the board: Deploy window', text: '{}' },
    { id: 'bead:p:p-ws.1', workstream: 'p:p-ws', when: 'yesterday', label: 'Step finished: Build the retry key', text: '{}' },
    { id: 'goal:p:p-ws', workstream: 'p:p-ws', when: 'board', label: 'The board\'s record of this workstream', text: '{}' },
  ],
  brief: {
    generated_at: '2026-11-20T12:00:00Z',
    windows: {
      now: '2026-11-20T07:00:00-05:00',
      today: { start: '', end: '', label: 'Friday, Nov 20' },
      yesterday: { start: '', end: '', label: 'Thursday, Nov 19' },
      week: { start: '', end: '', label: 'Mon Nov 16 to now' },
    },
    today: [{ workstream: 'p:p-ws', name: 'Billing', sentences: [{ text: "I'm rolling out the retry fix in today's deploy window.", sources: ['step:p-ws.2.1'] }] }],
    yesterday: [{ workstream: 'p:p-ws', name: 'Billing', sentences: [{ text: 'I built the retry key.', sources: ['bead:p:p-ws.1'] }] }],
    week: [{ workstream: 'p:p-ws', name: 'Billing', progress: 60, sentences: [{ text: 'Retries that charge once are about 60% there.', sources: ['goal:p:p-ws'] }] }],
  },
}

let calls: { url: string; method: string }[] = []
let next: BriefStatus = ready

beforeEach(() => {
  calls = []
  next = ready
  useBrief.setState({ status: null, failed: false })
  useStore.setState({ config: { apiUrl: 'http://localhost:8787', token: 't' } })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), method: init?.method ?? 'GET' })
    return new Response(JSON.stringify(next), { status: 200 })
  }))
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  useStore.setState({ selected: { kind: 'commander' }, focus: null, narrow: false, chatTab: true })
})

describe('the Brief tab', () => {
  it('sits right under Progress on the rail, is a page, and the bar names it', () => {
    render(<><TopBar left={397} chat={640} /><Rail /></>)
    const buttons = screen.getAllByRole('button').map((b) => b.getAttribute('aria-label'))
    expect(buttons.indexOf('Brief')).toBe(buttons.indexOf('Progress') + 1)
    const tab = screen.getByRole('button', { name: 'Brief' })
    const outline = tab.innerHTML
    fireEvent.click(tab)
    expect(useStore.getState().selected).toEqual({ kind: 'brief' })
    expect(tab).toHaveClass('on')
    expect(tab.innerHTML).not.toBe(outline) // the filled icon when selected
    expect(screen.getByText('Brief', { selector: '.app-top-title' })).toBeInTheDocument()
    expect(isPage({ kind: 'brief' })).toBe(true)
    expect(homeActive({ kind: 'brief' })).toBe(false)
    act(() => useStore.getState().goBack())
    expect(useStore.getState().selected).toEqual({ kind: 'commander' })
  })

  it('shows the three sections in order with when it was written', async () => {
    render(<BriefView />)
    await screen.findByText('I built the retry key.')
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.firstChild?.textContent)
    expect(headings).toEqual(['Moving today', 'Done yesterday', 'This week'])
    expect(screen.getByText(/^Written /)).toBeInTheDocument()
    expect(within(screen.getByRole('region', { name: 'Done yesterday' })).getByText('Thursday, Nov 19')).toBeInTheDocument()
    expect(screen.getByText("I'm rolling out the retry fix in today's deploy window.")).toHaveAttribute('title', 'Now on the board: Deploy window')
    const week = screen.getByRole('region', { name: 'This week' })
    expect(within(week).getByText('60%')).toBeInTheDocument()
    expect(calls[0]).toEqual({ url: 'http://localhost:8787/api/brief', method: 'GET' })
  })

  it('Regenerate asks cmdrd for a new one and shows it is writing', async () => {
    render(<BriefView />)
    await screen.findByText('I built the retry key.')
    next = { ...ready, generating: true }
    fireEvent.click(screen.getByRole('button', { name: 'Regenerate' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Writing…' })).toBeDisabled())
    expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/api/brief'))).toBe(true)
    expect(screen.getByText('I built the retry key.')).toBeInTheDocument() // the last Brief stays while it writes
  })

  it('before the first Brief, says when it will be written', async () => {
    next = { generating: false, next_at: '2026-11-20T12:00:00Z', evidence: [] }
    render(<BriefView />)
    expect(await screen.findByText(/writes the Brief at 7 AM, 1 PM, 7 PM and 1 AM Eastern/)).toBeInTheDocument()
    expect(screen.getByText(/Not written yet/)).toBeInTheDocument()
  })
})
