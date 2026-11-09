// IPC contract between the inspector renderer (via the preload bridge) and
// the main process. No electron imports, so preload, main and tests share it.
// The renderer side mirrors these shapes in src/features/inspector/types.ts.

import type { Rect } from './policy'

export const CH = {
  sync: 'inspector:sync', // renderer -> main (send): SyncMsg
  nav: 'inspector:nav', // renderer -> main (send): NavMsg
  openExternal: 'inspector:open-external', // renderer -> main (send): url
  signin: 'inspector:signin', // renderer -> main (invoke): Record<siteId, boolean>
  capture: 'inspector:capture', // renderer -> main (invoke): CaptureReq -> CaptureResult (the shown tab only)
  state: 'inspector:state', // main -> renderer: ViewState
  shortcut: 'inspector:shortcut', // main -> renderer: ShortcutMsg
} as const

// SyncMsg is declarative: main makes its views match it. tabs are the web
// tabs that exist; activeId is the one to show at rect (CSS px of the host
// page); rect null hides every view (no web tab active, or a menu overlaps).
export type SyncMsg = {
  tabs: Array<{ id: number; url: string }>
  activeId: number | null
  rect: Rect | null
}

export type NavMsg = {
  id: number
  action: 'back' | 'forward' | 'reload' | 'stop' | 'load' | 'devtools'
  url?: string // for load
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

export type CaptureReq = { id: number; block: string[]; expect?: string }

export type ShortcutMsg = { id: number; action: 'focus-url' }
