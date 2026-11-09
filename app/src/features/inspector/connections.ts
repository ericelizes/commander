// Connections: the sites the panel's web tabs are signed in to. The sidebar
// shows them; signing in opens the site's login page as a tab in the panel.

import { SIGNIN_SITES } from './model'
import type { WebviewBridge } from './types'

export { SIGNIN_SITES }

// OPEN_TAB_EVENT asks the panel to open a page as a tab (detail: { url, title }).
// The panel claims it with preventDefault.
export const OPEN_TAB_EVENT = 'cmdr:open-tab'

// openInPanel opens url as a tab in the panel. With no panel on screen (nothing
// in focus: the Commander chat alone) nobody claims it and it opens in the browser.
export function openInPanel(url: string, title?: string) {
  const claimed = !window.dispatchEvent(new CustomEvent(OPEN_TAB_EVENT, { detail: { url, title }, cancelable: true }))
  if (claimed) return
  const b = bridge()
  if (b) b.openExternal(url)
  else window.open(url, '_blank', 'noopener,noreferrer')
}

function bridge(): WebviewBridge | undefined {
  return typeof window !== 'undefined' ? (window as { commanderHost?: { webview?: WebviewBridge } }).commanderHost?.webview : undefined
}

/** hasConnections is whether this app can show signed-in web pages (the desktop app can; a browser tab cannot). */
export function hasConnections(): boolean {
  return !!bridge()
}

/** signinStatus is which sites are signed in, by site id. */
export function signinStatus(): Promise<Record<string, boolean>> {
  return bridge()?.signinStatus() ?? Promise.resolve({})
}
