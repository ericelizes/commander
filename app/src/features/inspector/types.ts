// Shapes of cmdrd's /api/tabs responses (internal/api/tabs.go) and of the
// Electron bridge (app/electron/inspector/contract.ts, mirrored here so the
// renderer has no electron imports).

export type TabView = 'web' | 'card' | 'decision'

export type TabInfo = {
  id: number
  position: number
  title: string
  kind: 'url' | 'cmd' | 'decision' | string
  target: string
  renderer: string
  detected: string
  view: TabView
  refresh_secs: number
  added_by: string
  created_at: number
}

export type TabList = {
  project_id: number
  ws: string
  active_id: number
  tabs: TabInfo[]
}

// inspector.Card (Go, untagged fields => PascalCase JSON).
export type Card = {
  Renderer: string
  Title: string
  State: string
  URL: string
  Meta: string[] | null
  Sections: Array<{ Title: string; Lines: string[] | null }> | null
  Markdown: string
  Preformatted: boolean
  Attn: boolean
  UpdatedAt: string
  FetchedAt: string
  Err: string
}

export type CardResponse = {
  tab_id: number
  card: Card
  stale: boolean
  refreshing: boolean
}

export type CmdResult = { ok: boolean; stdout: string; stderr: string; exit: number; error?: string }

export type Rect = { x: number; y: number; width: number; height: number }

export type SyncMsg = {
  tabs: Array<{ id: number; url: string }>
  activeId: number | null
  rect: Rect | null
}

export type NavMsg = {
  id: number
  action: 'back' | 'forward' | 'reload' | 'stop' | 'load' | 'devtools'
  url?: string
}

export type ViewState = {
  id: number
  url: string
  title: string
  loading: boolean
  error: string | null
  canGoBack: boolean
  canGoForward: boolean
  favicon?: string // the page's own icon, as a data: URL (the renderer's CSP allows no remote images)
}

// WebviewBridge is window.commanderHost.webview, present only in Electron.
export type WebviewBridge = {
  sync(msg: SyncMsg): void
  nav(msg: NavMsg): void
  openExternal(url: string): void
  /** Picture + text of the shown web tab `id`; refused when it is not shown or matches `block`. */
  capture?(req: { id: number; block: string[]; expect?: string }): Promise<
    { ok: true; capture: { url: string; title: string; png: string; text: string } } | { ok: false; reason: 'blocked' | 'not-shown' | 'failed' }
  >
  signinStatus(): Promise<Record<string, boolean>>
  onState(cb: (s: ViewState) => void): () => void
  onShortcut(cb: (s: { id: number; action: 'focus-url' }) => void): () => void
}

// The host page declares window.commanderHost (src/renderer/src/host.ts);
// this feature only reads its optional .webview.
export type HostWithWebview = { commanderHost?: { webview?: WebviewBridge } }

// InspectorApi is how the pane reaches cmdrd.
export type InspectorApi = {
  baseUrl: string // e.g. http://127.0.0.1:8787 ("" = same origin, web mode)
  token: string
  fetch?: typeof fetch
}

// The inspector window: the Commander window is { } (no ws).
export type InspectorWindow = { ws?: string; project?: string }
