// Where the window's page comes from. The live renderer dev server
// (`npm run dev:renderer` on 127.0.0.1:5173/app/, kept up by the commander-ui
// launchd/systemd unit) wins while it answers, so an edit under app/src shows
// up through Vite HMR in the installed app with no rebuild or reinstall. When
// it is not there (or dies) the window shows a plain "Connecting" page (never
// the bundled renderer, which is as old as the app build), and a poll every 3s
// switches to live once it answers.
//
// Only the page changes: the preload (window.commanderHost / commanderShell)
// is bundled in the app and attaches to whatever the window loads.
// $COMMANDER_APP_DEV_URL overrides the dev server origin ("off" = bundled only).

export type RendererSource = 'live' | 'bundled' | 'connecting'

export const DEFAULT_DEV_ORIGIN = 'http://127.0.0.1:5173'
export const POLL_MS = 3000

/** devRendererURL is the dev server page to load, or undefined when live mode is off. */
export function devRendererURL(env: NodeJS.ProcessEnv = process.env): string | undefined {
  const v = env.COMMANDER_APP_DEV_URL
  if (v === 'off') return undefined
  return `${(v || DEFAULT_DEV_ORIGIN).replace(/\/+$/, '')}/app/`
}

/** devServerUp: the URL answers 200 with a Vite page (it loads /@vite/client), not just any server on the port. */
export async function devServerUp(url: string, fetchImpl: typeof fetch = fetch, timeoutMs = 2500): Promise<boolean> {
  try {
    const res = await fetchImpl(url, { signal: AbortSignal.timeout(timeoutMs) })
    if (res.status !== 200) return false
    return (await res.text()).includes('/@vite/client')
  } catch {
    return false
  }
}

export interface SourceWindow {
  isDestroyed(): boolean
  loadURL(url: string): Promise<void>
  loadFile(path: string): Promise<void>
  webContents: {
    on(event: 'did-fail-load', listener: (e: unknown, code: number, desc: string, url: string, isMainFrame: boolean) => void): unknown
  }
}

export interface SourceOptions {
  devUrl: string
  bundle: string // dist/index.html
  waiting?: string // a page URL shown while the dev server is not there; without it, the bundle
  probe?: (url: string) => Promise<boolean>
  onChange?: (s: RendererSource) => void
  intervalMs?: number
  setInterval?: (fn: () => void, ms: number) => unknown
  clearInterval?: (t: unknown) => void
}

export interface SourceWatch {
  source(): RendererSource | undefined
  tick(): Promise<void> // one probe + switch (the interval calls it)
  stop(): void
}

const ERR_ABORTED = -3 // a load replaced by the next one: not a failure

const MISSES_TO_FALL_BACK = 4

export function watchRendererSource(win: SourceWindow, opts: SourceOptions): SourceWatch {
  const probe = opts.probe ?? ((u: string) => devServerUp(u))
  const every = opts.setInterval ?? ((fn, ms) => setInterval(fn, ms))
  const cancel = opts.clearInterval ?? ((t) => clearInterval(t as ReturnType<typeof setInterval>))
  let current: RendererSource | undefined
  let busy = false
  let stopped = false

  const show = async (s: RendererSource) => {
    if (stopped || win.isDestroyed() || current === s) return
    current = s
    opts.onChange?.(s)
    if (s === 'live') {
      try {
        await win.loadURL(opts.devUrl)
        return
      } catch {
        if (current !== 'live') return // a later switch already happened
        current = undefined
        return show(fallback)
      }
    }
    if (s === 'connecting' && opts.waiting) await win.loadURL(opts.waiting).catch(() => undefined)
    else await win.loadFile(opts.bundle).catch(() => undefined)
  }
  const fallback: RendererSource = opts.waiting ? 'connecting' : 'bundled'

  // One missed probe is not the dev server gone (a restart, a busy moment):
  // the live page stays until MISSES_TO_FALL_BACK in a row.
  let misses = 0
  const tick = async () => {
    if (busy || stopped) return
    busy = true
    try {
      const up = await probe(opts.devUrl)
      misses = up ? 0 : misses + 1
      if (up) await show('live')
      else if (current !== 'live' || misses >= MISSES_TO_FALL_BACK) await show(fallback)
    } finally {
      busy = false
    }
  }

  // The dev server died under the live page (or refused it): the bundle.
  win.webContents.on('did-fail-load', (_e, code, _desc, url, isMainFrame) => {
    if (!isMainFrame || code === ERR_ABORTED || current !== 'live' || !url.startsWith(opts.devUrl.replace(/\/app\/$/, ''))) return
    void show(fallback)
  })

  void tick()
  const timer = every(() => void tick(), opts.intervalMs ?? POLL_MS)
  return {
    source: () => current,
    tick,
    stop() {
      stopped = true
      cancel(timer)
    },
  }
}

export function sourceLabel(s: RendererSource | undefined): string {
  return s === 'live' ? 'Live (dev server)' : s === 'connecting' ? 'Connecting' : 'Bundled'
}

/** connectingPage: the plain page shown while the live renderer is not reachable, in the app's own colours. */
export function connectingPage(): string {
  const html = `<!doctype html><meta charset="utf-8"><title>Commander</title><style>
:root{color-scheme:light dark;--bg:#ecedf0;--fg:#5b6270}
@media (prefers-color-scheme:dark){:root{--bg:#131418;--fg:#9aa1ad}}
html,body{height:100%;margin:0;background:var(--bg);color:var(--fg);font:500 14px/1.4 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;-webkit-app-region:drag}
body{display:grid;place-items:center}
.d{display:flex;gap:10px;align-items:center}
.s{width:14px;height:14px;border:2px solid currentColor;border-right-color:transparent;border-radius:50%;animation:r .9s linear infinite}
@keyframes r{to{transform:rotate(360deg)}}
</style><div class="d"><span class="s"></span>Connecting to Commander…</div>`
  return `data:text/html;charset=utf-8,${encodeURIComponent(html)}`
}
