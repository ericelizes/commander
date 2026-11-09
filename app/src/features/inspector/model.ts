// Pure tab-model logic for the inspector pane (unit-tested; no React).

import type { Card, InspectorWindow, Rect, SyncMsg, TabInfo, ViewState } from './types'

// ---- commands (POST /api/cmd argv; the tab verbs are allow-listed) ----------

function windowFlags(win: InspectorWindow): string[] {
  const out: string[] = []
  if (win.ws) out.push('--ws', win.ws)
  if (win.ws && win.project) out.push('--project', win.project)
  return out
}

export function addTabArgv(win: InspectorWindow, url: string, title?: string): string[] {
  const argv = ['tab', 'add', '--url', url]
  if (title) argv.push('--title', title)
  return [...argv, '--focus', ...windowFlags(win)]
}

export function rmTabArgv(win: InspectorWindow, id: number): string[] {
  return ['tab', 'rm', `#${id}`, ...windowFlags(win)]
}

export function focusTabArgv(win: InspectorWindow, id: number): string[] {
  return ['tab', 'focus', `#${id}`, ...windowFlags(win)]
}

export function moveTabArgv(win: InspectorWindow, id: number, pos: number): string[] {
  return ['tab', 'move', `#${id}`, String(pos), ...windowFlags(win)]
}

// normalizeUrl turns what the user typed into an http(s) URL, or null.
// "linear.app/x" -> "https://linear.app/x"; "localhost:3000" -> http.
export function normalizeUrl(raw: string): string | null {
  const s = raw.trim()
  if (!s || /\s/.test(s)) return null
  let candidate = s
  if (!/^[a-z][a-z0-9+.-]*:\/\//i.test(s)) {
    const local = /^(localhost|127\.0\.0\.1)(:\d+)?(\/|$)/.test(s)
    candidate = (local ? 'http://' : 'https://') + s
  }
  try {
    const u = new URL(candidate)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
    if (!u.hostname.includes('.') && u.hostname !== 'localhost') return null
    return u.toString()
  } catch {
    return null
  }
}

// ---- how a tab's body is drawn ---------------------------------------------

export type BodyMode =
  | 'webview' // a native WebContentsView over the body rect (Electron)
  | 'card' // a text card from /api/tabs/card
  | 'link' // web mode: link card + Open in browser (+ digest card when one exists)
  | 'decision' // a decision form (host-provided)

// Renderers whose /api/tabs/card answer is a real digest, worth showing in
// web mode under the link card.
const DIGEST_RENDERERS = new Set(['notion', 'linear', 'github-pr'])

export function bodyMode(tab: TabInfo, hasWebview: boolean): BodyMode {
  if (tab.view === 'decision') return 'decision'
  if (tab.view === 'card') return 'card'
  return hasWebview ? 'webview' : 'link'
}

export function hasDigest(tab: TabInfo): boolean {
  return DIGEST_RENDERERS.has(tab.renderer || tab.detected)
}

// ---- active tab -------------------------------------------------------------

// resolveActive keeps a locally chosen tab (clicked, not yet confirmed by the
// server) while it still exists; otherwise the server's active tab.
export function resolveActive(tabs: TabInfo[], serverActive: number, pending: number | null): number | null {
  if (pending !== null && tabs.some((t) => t.id === pending)) return pending
  if (tabs.some((t) => t.id === serverActive)) return serverActive
  return tabs.length ? tabs[0].id : null
}

// neighborAfterClose is the tab to show after closing id: the next one, else
// the previous one.
export function neighborAfterClose(tabs: TabInfo[], id: number): number | null {
  const i = tabs.findIndex((t) => t.id === id)
  if (i < 0) return null
  return tabs[i + 1]?.id ?? tabs[i - 1]?.id ?? null
}

// ---- native view sync -------------------------------------------------------

export function syncMsg(tabs: TabInfo[], activeId: number | null, rect: Rect | null, hasWebview: boolean, hideViews: boolean): SyncMsg {
  if (!hasWebview) return { tabs: [], activeId: null, rect: null }
  const web = tabs.filter((t) => t.view === 'web').map((t) => ({ id: t.id, url: t.target }))
  const activeWeb = web.some((t) => t.id === activeId) ? activeId : null
  return { tabs: web, activeId: activeWeb, rect: activeWeb !== null && !hideViews ? rect : null }
}

// ---- icon and state dot -----------------------------------------------------

export type TabIcon = { label: string; site: string }

export function tabIcon(tab: TabInfo): TabIcon {
  if (tab.kind === 'decision') return { label: '⚑', site: 'decision' }
  if (tab.kind === 'cmd') return { label: '>_', site: 'cmd' }
  let host = ''
  try {
    host = new URL(tab.target).hostname
  } catch {
    // not a URL: generic icon
  }
  const r = tab.renderer || tab.detected
  if (r === 'notion' || /(^|\.)notion\.(so|site|com)$/.test(host)) return { label: 'N', site: 'notion' }
  if (r === 'linear' || host === 'linear.app') return { label: 'L', site: 'linear' }
  if (r === 'github-pr' || host === 'github.com') return { label: 'GH', site: 'github' }
  if (/(^|\.)datadoghq\.(com|eu)$/.test(host)) return { label: 'DD', site: 'datadog' }
  if (/(^|\.)slack\.com$/.test(host)) return { label: 'S', site: 'slack' }
  return { label: '◎', site: 'web' }
}

export type Dot = 'none' | 'loading' | 'ok' | 'attn' | 'error'

export function stateDot(tab: TabInfo, view?: ViewState, card?: { card: Card; refreshing: boolean }): Dot {
  if (tab.view === 'web') {
    if (!view) return 'none'
    if (view.error) return 'error'
    return view.loading ? 'loading' : 'ok'
  }
  if (!card) return 'none'
  if (card.card.Err) return 'error'
  if (card.card.Attn) return 'attn'
  return card.refreshing ? 'loading' : 'ok'
}

// ---- sign-in sites ----------------------------------------------------------

// Mirrors SITES in app/electron/inspector/policy.ts (ids and login pages).
export const SIGNIN_SITES = [
  { id: 'notion', name: 'Notion', loginUrl: 'https://www.notion.so/login' },
  { id: 'linear', name: 'Linear', loginUrl: 'https://linear.app/login' },
  { id: 'github', name: 'GitHub', loginUrl: 'https://github.com/login' },
  { id: 'datadog', name: 'Datadog', loginUrl: 'https://app.datadoghq.com/account/login' },
  { id: 'slack', name: 'Slack', loginUrl: 'https://app.slack.com/' },
] as const

// ---- card helpers -----------------------------------------------------------

export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return ''
  const t = Date.parse(iso)
  if (!Number.isFinite(t) || t <= 0) return ''
  const s = Math.max(0, (now - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 48 * 3600) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

// cardPollMs is how often the active card tab asks cmdrd again (the server
// only refetches when the card is stale, so asking is cheap).
export function cardPollMs(tab: TabInfo, refreshing: boolean): number {
  if (refreshing) return 1500
  return Math.min(Math.max(tab.refresh_secs, 5), 30) * 1000
}
