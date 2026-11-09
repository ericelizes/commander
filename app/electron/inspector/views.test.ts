import { EventEmitter } from 'node:events'

import { describe, expect, it, vi } from 'vitest'

// No Electron here: a stand-in view, so InspectorViews' own use of the guard is
// what runs (a dropped guardTab call or a reload that ends a session fails here).
vi.mock('electron', () => {
  class WebContentsView {
    webContents = new FakeContents()
    setBackgroundColor(): void {}
    setBounds(): void {}
    setVisible(): void {}
    getVisible(): boolean {
      return true
    }
  }
  return { WebContentsView, Menu: {}, shell: {}, clipboard: {} }
})
vi.mock('../context-menu', () => ({ attachContextMenu: () => {} }))

class FakeContents extends EventEmitter {
  url = ''
  loads: string[] = []
  reloads = 0
  navigationHistory = { canGoBack: () => true, canGoForward: () => true, goBack: () => {}, goForward: () => {} }
  openHandler: ((d: { url: string }) => { action: string }) | null = null
  getURL = (): string => this.url
  getTitle = (): string => ''
  isDestroyed = (): boolean => false
  close = (): void => {}
  stop = (): void => {}
  reload = (): void => {
    this.reloads++
  }
  loadURL = (u: string): Promise<void> => {
    this.loads.push(u)
    return Promise.resolve()
  }
  setWindowOpenHandler = (h: FakeContents['openHandler']): void => {
    this.openHandler = h
  }
  fire(event: 'will-navigate' | 'will-redirect', url: string): boolean {
    let stopped = false
    this.emit(event, { url, isMainFrame: true, preventDefault: () => (stopped = true) })
    return stopped
  }
}

const { InspectorViews } = await import('./views')

const home = 'https://box-1.term.example/'
const signIn = 'https://provider.example/login?redirect=' + encodeURIComponent('https://box-1.term.example/shell')
const verify = 'https://provider.example/login/verify'
const rect = { x: 0, y: 0, width: 800, height: 600 }

function tab(answer = true) {
  const opened: string[] = []
  const asked: string[] = []
  const win = { contentView: { addChildView: () => {}, removeChildView: () => {} }, webContents: { getZoomFactor: () => 1 }, getContentSize: () => [1000, 800] }
  const views = new InspectorViews(win as never, { session: {} as never, dev: false, openExternal: (u) => opened.push(u), providerGate: { trusted: () => false, ask: (provider) => (asked.push(provider), answer ? 'yes' : 'no') }, emitState: () => {}, emitShortcut: () => {} })
  views.sync({ tabs: [{ id: 1, url: home }], activeId: 1, rect })
  const wc = views.view(1)!.webContents as unknown as FakeContents
  return { views, wc, opened, asked }
}

// A tab whose own load redirected to the provider, now on its page.
function onProvider(answer = true) {
  const t = tab(answer)
  expect(t.wc.loads).toEqual([home])
  expect(t.wc.fire('will-redirect', signIn)).toBe(false)
  t.wc.url = signIn
  t.wc.emit('did-navigate')
  return t
}

describe('InspectorViews uses the guard', () => {
  it('guards a new tab: an ordinary link opens in the browser', () => {
    const { wc, opened } = tab()
    wc.url = home
    wc.emit('did-navigate')
    expect(wc.fire('will-navigate', 'https://news.example.org/')).toBe(true)
    expect(opened).toEqual(['https://news.example.org/'])
  })

  it('opens a sign-in window sandboxed, on the cockpit partition', () => {
    const { wc } = tab()
    wc.url = home
    wc.emit('input-event', {}, { type: 'mouseDown' })
    const verdict = wc.openHandler!({ url: signIn }) as { overrideBrowserWindowOptions?: { webPreferences?: Record<string, unknown> } }
    expect(verdict.overrideBrowserWindowOptions?.webPreferences).toMatchObject({ partition: 'persist:cockpit', sandbox: true, contextIsolation: true, nodeIntegration: false })
  })

  it('guards a new tab\'s popups', () => {
    const { wc, opened } = tab()
    expect(wc.openHandler!({ url: 'https://news.example.org/' })).toEqual({ action: 'deny' })
    expect(opened).toEqual(['https://news.example.org/'])
  })

  it('counts a new tab\'s first load as ours: its redirect starts a session', () => {
    const { wc, opened } = onProvider()
    expect(wc.fire('will-navigate', verify)).toBe(false)
    expect(opened).toEqual([])
  })

  it('passes a new tab\'s provider question to its options: a no opens the browser', () => {
    const { wc, opened } = onProvider(false)
    expect(wc.fire('will-navigate', verify)).toBe(true)
    expect(opened).toEqual([verify])
  })

  it('keeps the session through the reload action', () => {
    const { views, wc, opened } = onProvider()
    views.nav({ id: 1, action: 'reload' })
    expect(wc.reloads).toBe(1)
    wc.emit('did-navigate')
    expect(wc.fire('will-navigate', verify)).toBe(false)
    expect(opened).toEqual([])
  })

  it('keeps the session through crash recovery', () => {
    const { views, wc, opened } = onProvider()
    wc.emit('render-process-gone', {}, { reason: 'killed' })
    views.sync({ tabs: [{ id: 1, url: home }], activeId: 1, rect })
    expect(wc.loads).toEqual([home, signIn])
    wc.emit('did-navigate')
    expect(wc.fire('will-navigate', verify)).toBe(false)
    expect(opened).toEqual([])
  })

  it('a crash reload is the page\'s doing: a no stands, while the user\'s reload ends it (M1)', () => {
    const { views, wc, opened, asked } = onProvider(false)
    expect(wc.fire('will-navigate', verify)).toBe(true)
    wc.emit('render-process-gone', {}, { reason: 'killed' })
    views.sync({ tabs: [{ id: 1, url: home }], activeId: 1, rect })
    expect(wc.loads).toEqual([home, signIn])
    expect(wc.fire('will-redirect', signIn)).toBe(true)
    wc.emit('did-navigate')
    expect(wc.fire('will-navigate', verify)).toBe(true)
    expect(asked).toHaveLength(1)
    expect(opened).toEqual([verify])

    views.nav({ id: 1, action: 'reload' })
    expect(wc.fire('will-redirect', signIn)).toBe(false)
    wc.emit('did-navigate')
    expect(wc.fire('will-navigate', verify)).toBe(true)
    expect(asked).toHaveLength(2)
  })

  it('a crash reload with no no is our own load: its redirect is followed', () => {
    const { views, wc, opened, asked } = tab()
    wc.url = home
    wc.emit('did-navigate')
    wc.emit('render-process-gone', {}, { reason: 'killed' })
    views.sync({ tabs: [{ id: 1, url: home }], activeId: 1, rect })
    expect(wc.fire('will-redirect', 'https://news.example.org/')).toBe(false)
    expect(opened).toEqual([])
    expect(asked).toEqual([])
  })

  it('ends the session on a typed address, and on a changed tab address', () => {
    const typed = onProvider()
    typed.views.nav({ id: 1, action: 'load', url: 'https://provider.example/typed' })
    typed.wc.emit('did-navigate')
    expect(typed.wc.fire('will-navigate', verify)).toBe(true)
    expect(typed.opened).toEqual([verify])

    const moved = onProvider()
    moved.views.sync({ tabs: [{ id: 1, url: 'https://other.example/' }], activeId: 1, rect })
    moved.wc.emit('did-navigate')
    expect(moved.wc.fire('will-navigate', verify)).toBe(true)
    expect(moved.opened).toEqual([verify])
  })
})
