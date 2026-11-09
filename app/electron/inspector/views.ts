// InspectorViews owns one WebContentsView per web tab, all on the persistent
// cockpit partition, laid over the renderer's inspector body rect.

import { attachContextMenu } from '../context-menu'
import { WebContentsView, type BrowserWindow, type Session, type WebContents } from 'electron'

import type { NavMsg, ShortcutMsg, SyncMsg, ViewState } from './contract'
import { freshNav, guardTab, ours, oursKeepingSession, recovering, type NavState, type ProviderGate } from './guard'
import { clampBounds, decideCrash, noteReload, PARTITION, viewTooSmall, type CrashRecovery, type Rect } from './policy'
import { capturePage, type CaptureResult } from './capture'
import { planSync } from './reconcile'

export type ViewsOptions = {
  session: Session
  dev: boolean
  openExternal: (url: string) => void
  providerGate: ProviderGate
  emitState: (s: ViewState) => void
  emitShortcut: (s: ShortcutMsg) => void
}

// crashed: the renderer is gone and the tab reloads when next shown.
type Entry = { view: WebContentsView; home: string; state: ViewState; crashed: boolean; recovery: CrashRecovery; nav: NavState }

const freshRecovery = (): CrashRecovery => ({ lastReloadAt: null, reloads: 0 })

// Chromium's "navigation aborted" (a new navigation replaced it): not an error.
const ERR_ABORTED = -3

// Shown by the renderer's overlay (with Open in browser) instead of a blank view.
export const TOO_SMALL = 'The pane is too small to show this page: widen it'

const remotePrefs = {
  partition: PARTITION,
  nodeIntegration: false,
  contextIsolation: true,
  sandbox: true,
  webSecurity: true,
  // no preload: remote pages get nothing from us
}

export class InspectorViews {
  private entries = new Map<number, Entry>()
  private shown: number | null = null
  private lastRect: Rect | null = null

  constructor(
    private win: BrowserWindow,
    private opts: ViewsOptions,
  ) {}

  ids(): number[] {
    return [...this.entries.keys()]
  }

  view(id: number): WebContentsView | undefined {
    return this.entries.get(id)?.view
  }

  // capture reads the web tab that is showing, and only that one.
  async capture(id: number, block: string[], expect?: string): Promise<CaptureResult> {
    const e = this.entries.get(id)
    if (!e || e.view.webContents.isDestroyed()) return { ok: false, reason: 'not-shown' }
    return capturePage(e.view.webContents, id === this.shown && e.view.getVisible(), block, expect)
  }

  sync(msg: SyncMsg): void {
    const existing = new Map([...this.entries].map(([id, e]) => [id, e.home]))
    const plan = planSync(existing, msg)
    for (const id of plan.destroy) this.destroy(id)
    for (const { id, url } of plan.reload) {
      const e = this.entries.get(id)!
      e.home = url
      ours(e.nav)
      void e.view.webContents.loadURL(url).catch(() => {})
    }
    for (const id of plan.create) {
      this.create(id, msg.tabs.find((t) => t.id === id)!.url)
    }
    this.shown = plan.show
    this.lastRect = msg.rect
    this.layout()
  }

  // layout shows the active view at the last rect (scaled by the host page's
  // zoom), unless it is in an error state: then the renderer's overlay shows.
  private layout(): void {
    const zoom = this.win.webContents.getZoomFactor() || 1
    const [width, height] = this.win.getContentSize()
    for (const [id, e] of this.entries) {
      const small = id === this.shown && this.lastRect !== null && viewTooSmall(this.lastRect)
      if (small && e.state.error === null) this.update(e, { error: TOO_SMALL })
      else if (!small && e.state.error === TOO_SMALL) this.update(e, { error: null })
      const visible = id === this.shown && this.lastRect !== null && e.state.error === null
      if (visible && this.lastRect) {
        const r = this.lastRect
        e.view.setBounds(clampBounds({ x: r.x * zoom, y: r.y * zoom, width: r.width * zoom, height: r.height * zoom }, { width, height }))
      }
      e.view.setVisible(visible)
      if (visible && e.crashed) this.recover(e)
    }
  }

  // recover reloads a tab whose renderer is gone; loadURL starts a new one.
  private recover(e: Entry): void {
    e.crashed = false
    e.recovery = noteReload(e.recovery, Date.now())
    this.update(e, { loading: true, error: null })
    recovering(e.nav) // the same page again: a sign-in on it carries on, a no stands
    void e.view.webContents.loadURL(e.view.webContents.getURL() || e.state.url || e.home).catch(() => {})
  }

  nav(msg: NavMsg): void {
    const e = this.entries.get(msg.id)
    if (!e) return
    const wc = e.view.webContents
    if (msg.action === 'reload') oursKeepingSession(e.nav)
    else if (msg.action !== 'stop' && msg.action !== 'devtools') ours(e.nav)
    switch (msg.action) {
      case 'back':
        if (wc.navigationHistory.canGoBack()) wc.navigationHistory.goBack()
        break
      case 'forward':
        if (wc.navigationHistory.canGoForward()) wc.navigationHistory.goForward()
        break
      case 'reload':
        e.crashed = false
        e.recovery = freshRecovery()
        this.update(e, { error: null })
        this.layout()
        if (wc.getURL()) wc.reload()
        else void wc.loadURL(e.home).catch(() => {})
        break
      case 'stop':
        wc.stop()
        break
      case 'load':
        if (msg.url) {
          // Typed into the URL field: allowed even if off-site; this is the user navigating.
          e.crashed = false
          e.recovery = freshRecovery()
          this.update(e, { error: null })
          this.layout()
          void wc.loadURL(msg.url).catch(() => {})
        }
        break
      case 'devtools':
        if (this.opts.dev) wc.openDevTools({ mode: 'detach' })
        break
    }
  }

  private create(id: number, url: string): void {
    const view = new WebContentsView({ webPreferences: remotePrefs })
    view.setBackgroundColor('#ffffff')
    const e: Entry = {
      view,
      home: url,
      state: { id, url, title: '', loading: true, error: null, canGoBack: false, canGoForward: false },
      crashed: false,
      recovery: freshRecovery(),
      nav: { ...freshNav(), ours: true },
    }
    this.entries.set(id, e)
    this.win.contentView.addChildView(view)
    this.wire(e)
    void view.webContents.loadURL(url).catch(() => {}) // failures arrive as did-fail-load
  }

  private destroy(id: number): void {
    const e = this.entries.get(id)
    if (!e) return
    this.entries.delete(id)
    this.win.contentView.removeChildView(e.view)
    if (!e.view.webContents.isDestroyed()) e.view.webContents.close()
  }

  dispose(): void {
    for (const id of this.ids()) this.destroy(id)
  }

  // favicon fetches the page's icon in the page's own session (so signed-in
  // sites serve it) and hands it on as a data: URL; a big or failed one is skipped.
  private async favicon(e: Entry, url: string): Promise<void> {
    try {
      const res = await e.view.webContents.session.fetch(url)
      const type = res.headers.get('content-type') ?? ''
      if (!res.ok || !type.startsWith('image/')) return
      const buf = Buffer.from(await res.arrayBuffer())
      if (buf.length > 64 * 1024) return
      this.update(e, { favicon: `data:${type.split(';')[0]};base64,${buf.toString('base64')}` })
    } catch {
      // no icon: the tab keeps its letter mark
    }
  }

  private update(e: Entry, patch: Partial<ViewState>): void {
    const wc = e.view.webContents
    if (wc.isDestroyed()) return
    e.state = {
      ...e.state,
      url: wc.getURL() || e.state.url,
      title: wc.getTitle() || e.state.title,
      canGoBack: wc.navigationHistory.canGoBack(),
      canGoForward: wc.navigationHistory.canGoForward(),
      ...patch,
    }
    this.opts.emitState(e.state)
  }

  private wire(e: Entry): void {
    const wc: WebContents = e.view.webContents
    const id = e.state.id
    attachContextMenu(wc)

    wc.on('page-favicon-updated', (_ev, urls) => {
      const url = urls[0]
      if (url) void this.favicon(e, url)
    })

    // One guard for every step of the tab and its sign-in windows: will-navigate,
    // will-redirect and window.open all go through the sign-in session model.
    guardTab(wc, () => e.home, e.nav, this.opts.openExternal, () => ({
      parent: this.win,
      width: 520,
      height: 720,
      autoHideMenuBar: true,
      webPreferences: remotePrefs,
    }), this.opts.providerGate)

    wc.on('did-start-loading', () => this.update(e, { loading: true }))
    wc.on('did-stop-loading', () => this.update(e, { loading: false }))
    wc.on('page-title-updated', () => this.update(e, {}))
    wc.on('did-navigate', () => this.update(e, { error: null }))
    wc.on('did-navigate-in-page', () => this.update(e, {}))
    wc.on('did-fail-load', (_ev, code, desc, validatedURL, isMainFrame) => {
      if (!isMainFrame || code === ERR_ABORTED) return
      this.update(e, { loading: false, error: `${desc || 'load failed'} (${code})`, url: validatedURL })
      this.layout()
    })
    wc.on('render-process-gone', (_ev, details) => {
      if (decideCrash(e.recovery, Date.now()) === 'give-up') {
        this.update(e, { loading: false, error: `page keeps crashing (${details.reason})` })
      } else {
        // A background tab waits until it is shown; layout reloads the shown one now.
        e.crashed = true
        this.update(e, { loading: false, error: null })
        setImmediate(() => this.layout()) // not from inside the crash event
        return
      }
      this.layout()
    })

    // Shortcuts while the webview has focus (it swallows keys the host page
    // would see): Cmd/Ctrl+L focuses the URL field, +R reloads, +[ ] go
    // back/forward, Cmd+Alt+I opens devtools in dev.
    wc.on('before-input-event', (ev, input) => {
      if (input.type !== 'keyDown') return
      const mod = process.platform === 'darwin' ? input.meta : input.control
      if (!mod) return
      const key = input.key.toLowerCase()
      if (key === 'l') {
        ev.preventDefault()
        this.opts.emitShortcut({ id, action: 'focus-url' })
      } else if (key === 'r') {
        ev.preventDefault()
        this.nav({ id, action: 'reload' })
      } else if (key === '[') {
        ev.preventDefault()
        this.nav({ id, action: 'back' })
      } else if (key === ']') {
        ev.preventDefault()
        this.nav({ id, action: 'forward' })
      } else if (key === 'i' && input.alt && this.opts.dev) {
        ev.preventDefault()
        this.nav({ id, action: 'devtools' })
      }
    })
  }
}
