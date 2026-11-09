import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UpdateBanner } from './UpdateBanner'
import { bannerText, installRoute, showBanner, useUpdateStore, type UpdateStatus } from '../updateApi'
import { useStore } from '../store'

const base: UpdateStatus = {
  running: 'a',
  installed: 'a',
  main: 'b',
  behind: 3,
  up_to_date: false,
  dirty: false,
  can_rollback: true,
  commands: ['cd /repo', 'just build', 'just install --yes --restart-cmdrd'],
  commits: [
    { hash: '1111111aaaa', subject: 'feat: one', review: 'reviewed', evidence: 'bead x' },
    { hash: '2222222bbbb', subject: 'fix: two', review: 'not confirmed' },
  ],
}

beforeEach(() => {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false })))
})
afterEach(() => {
  cleanup()
  useUpdateStore.setState({ status: null, panelOpen: false, appInstall: null })
  delete (window as any).commanderShell
  vi.unstubAllGlobals()
})

describe('update helpers', () => {
  it('hides when up to date and shows N commits ahead otherwise', () => {
    expect(showBanner(null)).toBe(false)
    expect(showBanner({ ...base, behind: 0, commits: [], up_to_date: true })).toBe(false)
    expect(showBanner({ ...base, error: 'x' })).toBe(false)
    expect(bannerText(base)).toBe('Update available: main is 3 commits ahead')
    expect(bannerText({ ...base, behind: 1 })).toBe('Update available: main is 1 commit ahead')
  })

  it('keeps a finished or failed job visible', () => {
    const job = { id: 'j', action: 'install' as const, events: [] }
    expect(showBanner({ ...base, behind: 0, job: { ...job, state: 'done' } })).toBe(true)
    expect(bannerText({ ...base, job: { ...job, state: 'running' } })).toBe('Installing…')
    expect(bannerText({ ...base, job: { ...job, state: 'failed' } })).toBe('Update failed')
    expect(bannerText({ ...base, job: { ...job, state: 'confirming' } })).toBe('Waiting for your confirmation on screen…')
  })
})

const config = { apiUrl: 'http://127.0.0.1:8787', token: 'tok' } as any

function open() {
  useStore.setState({ config })
  useUpdateStore.setState({ status: base })
  render(<UpdateBanner />)
  fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
}

describe('UpdateBanner', () => {
  it('renders nothing before there is a status', () => {
    const { container } = render(<UpdateBanner />)
    expect(container.querySelector('.side-update')).toBeNull()
  })

  it('opens a plain-language panel: what is new, with Not now and Update', () => {
    open()
    expect(screen.getByText('An update is ready')).toBeTruthy()
    expect(screen.getByText('One')).toBeTruthy()
    expect(screen.getByText('Two')).toBeTruthy()
    // Commit subjects, review markers and terminal commands are not part of the dialog.
    expect(screen.queryByText('feat: one')).toBeNull()
    expect(screen.queryByText('Reviewed')).toBeNull()
    expect(screen.queryByText(/just build/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Not now' })).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Update' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('counts commits that are not user-facing as smaller improvements', () => {
    useStore.setState({ config })
    useUpdateStore.setState({ status: { ...base, commits: [{ hash: '3333333cccc', subject: 'refactor: x', review: 'not confirmed' }] } })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
    expect(screen.getByText('1 smaller improvements')).toBeTruthy()
  })

  it("Update only asks cmdrd; the confirmation is cmdrd's own dialog", async () => {
    const calls: any[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: any) => {
        calls.push([url, init?.method ?? 'GET', init?.body, init?.headers])
        if (init?.method === 'POST') return { ok: true, status: 202, json: async () => ({ ok: true }) }
        return { ok: true, json: async () => ({ ...base, job: { id: 'j', action: 'install', state: 'confirming', events: [] } }) }
      }),
    )
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Update' }))
    await vi.waitFor(() => expect(screen.getByText('Confirm the update')).toBeTruthy())
    expect(screen.getByText(/Click Install in the window that just opened/)).toBeTruthy()
    const post = calls.find((c) => c[1] === 'POST')
    expect(post[0]).toBe('http://127.0.0.1:8787/api/update/install')
    expect(JSON.parse(post[2])).toEqual({ action: 'install' })
    expect(post[3]['X-Commander-Origin']).toBe('app') // wording only, not protection
    // While cmdrd waits for the person, the panel offers no second Update.
    expect(screen.queryByRole('button', { name: 'Update' })).toBeNull()
  })

  it('shows why a refused install did not start (429, no dialog tool, busy)', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 429, json: async () => ({ error: 'a confirmation was just shown; try again in a few minutes' }) })))
    open()
    fireEvent.click(screen.getByRole('button', { name: 'Update' }))
    expect((await screen.findByRole('alert')).textContent).toContain('a confirmation was just shown')
  })

  it('a declined job reads as available again and the panel offers Update', () => {
    const job = { id: 'j', action: 'install' as const, state: 'declined' as const, events: [] }
    expect(bannerText({ ...base, job })).toBe('Update available: main is 3 commits ahead')
    useStore.setState({ config })
    useUpdateStore.setState({ status: { ...base, job } })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
    expect(screen.getByText('An update is ready')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Update' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('has no Roll back in the dialog, whether or not a previous build exists', () => {
    open()
    expect(screen.queryByRole('button', { name: 'Roll back' })).toBeNull()
    cleanup()
    useUpdateStore.setState({ status: { ...base, can_rollback: false } })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
    expect(screen.queryByRole('button', { name: 'Roll back' })).toBeNull()
  })

  it('shows Try again and the failure after a failed job, and the done view once up to date', () => {
    const job = { id: 'j', action: 'install' as const, state: 'failed' as const, error: 'build broke', events: [{ step: 'build', text: 'oops' }] as any }
    useStore.setState({ config })
    useUpdateStore.setState({ status: { ...base, job } })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update failed' }))
    expect(screen.getByText("The update didn't finish")).toBeTruthy()
    expect(screen.getByText(/build broke/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy()
    cleanup()
    useUpdateStore.setState({ status: { ...base, behind: 0, commits: [], up_to_date: true, job: { ...job, state: 'done' } }, panelOpen: true })
    render(<UpdateBanner />)
    expect(screen.getByText("You're up to date")).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
  })
})

describe('install route', () => {
  it('uses the app when cmdrd is remote or headless and the app can run it, else cmdrd', () => {
    expect(installRoute(base, 'laptop', true)).toBe('cmdrd')
    expect(installRoute({ ...base, host: 'laptop' }, 'laptop', true)).toBe('cmdrd')
    expect(installRoute({ ...base, host: 'build-box' }, 'laptop', true)).toBe('app')
    expect(installRoute({ ...base, host: 'laptop', no_dialog: 'no display' }, 'laptop', true)).toBe('app')
    // A plain web page (or an older app without runUpdate) keeps cmdrd's path.
    expect(installRoute({ ...base, host: 'build-box', no_dialog: 'no display' }, 'laptop', false)).toBe('cmdrd')
  })
})

describe('UpdateBanner with cmdrd on a headless machine', () => {
  const remote = { ...base, host: 'build-box', no_dialog: 'no display' }

  it('Update goes to the app (its own dialog + SSH), never to cmdrd, and shows the failure', async () => {
    const fetchCalls: any[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: any) => {
      fetchCalls.push([url, init?.method ?? 'GET'])
      const after = url.endsWith('?fresh=1') ? { behind: 0, commits: [], up_to_date: true } : {}
      return { ok: true, json: async () => ({ ...remote, ...after }) }
    }))
    let finish!: (r: any) => void
    const runUpdate = vi.fn((_action: string, onLine: (l: string) => void) => {
      onLine('building cmdr')
      return new Promise((r) => { finish = r })
    })
    ;(window as any).commanderShell = { runUpdate }
    useStore.setState({ config: { ...config, localHost: 'laptop' } })
    useUpdateStore.setState({ status: remote })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
    fireEvent.click(screen.getByRole('button', { name: 'Update' }))
    await screen.findByText('Updating…', { selector: 'p' })
    expect(runUpdate).toHaveBeenCalledWith('install', expect.any(Function))
    // Working: no second Update.
    expect(screen.queryByRole('button', { name: 'Update' })).toBeNull()
    finish({ ok: false, error: 'the install over SSH exited with code 1', output: ['building cmdr'] })
    await screen.findByText("The update didn't finish")
    expect(screen.getByText(/the install over SSH exited with code 1/)).toBeTruthy()
    expect(screen.getByText(/building cmdr/)).toBeTruthy()
    expect(fetchCalls.some((c) => c[1] === 'POST')).toBe(false)
    await vi.waitFor(() => expect(fetchCalls.some((c) => c[0].endsWith('?fresh=1'))).toBe(true))
    // Still shown after the refresh says nothing is behind; Done clears it.
    expect(screen.getByText("The update didn't finish")).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(useUpdateStore.getState().appInstall).toBeNull()
  })

  it("a plain web page keeps cmdrd's path and shows its refusal", async () => {
    const calls: any[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: any) => {
      calls.push([url, init?.method ?? 'GET'])
      return { ok: false, status: 409, json: async () => ({ error: 'no confirmation dialog can be shown here (no display); run the terminal commands instead' }) }
    }))
    useStore.setState({ config: { ...config, localHost: 'laptop' } })
    useUpdateStore.setState({ status: remote })
    render(<UpdateBanner />)
    fireEvent.click(screen.getByRole('button', { name: 'Update available: main is 3 commits ahead' }))
    fireEvent.click(screen.getByRole('button', { name: 'Update' }))
    expect((await screen.findByRole('alert')).textContent).toContain('terminal commands')
    expect(calls.find((c) => c[1] === 'POST')[0]).toBe('http://127.0.0.1:8787/api/update/install')
  })
})
