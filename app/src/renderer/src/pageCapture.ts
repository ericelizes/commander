// What the user's focused web tab shows, attached to a message to Commander or
// a Manager so the agent sees pages behind his logins. Only the tab showing in
// the right panel, never another window; never a URL on his block list; only in
// Electron (the web page has no views to read). The files go through the chat's
// normal attachments (cmdrd saves them under the commander home, 0600, and
// deletes them after a day); the <user-screen> note says what was attached.

import { useSyncExternalStore } from 'react'

import type { Attachment } from '../../features/chat/transport'
import type { HostWithWebview } from '../../features/inspector/types'
import { invalidPatterns, isBlocked, originPath, parsePatterns } from '../../shared/captureBlock'

export const CAPTURE_TIMEOUT_MS = 2000
const KEY = 'cmdr.capture.block'
const ON_KEY = 'cmdr.capture.on'

export type Web = { id: number; url: string; title: string }
// off: a web tab is showing but sharing is turned off (the + menu turns it on).
export type Preview = { host: string; title: string; blocked: boolean; off: boolean }

let web: Web | null = null
let version = 0
const subs = new Set<() => void>()
const bump = () => {
  version++
  subs.forEach((f) => f())
}

const bridge = () => (typeof window === 'undefined' ? undefined : (window as unknown as HostWithWebview).commanderHost?.webview)

/** setWeb records the web tab showing in the right panel (InspectorColumn calls it). */
export function setWeb(next: Web | null) {
  if (next?.id === web?.id && next?.url === web?.url && next?.title === web?.title) return
  web = next
  bump()
}

export function blockText(): string {
  try {
    return localStorage.getItem(KEY) ?? ''
  } catch {
    return ''
  }
}

export function setBlockText(text: string) {
  try {
    localStorage.setItem(KEY, text)
  } catch {
    /* storage unavailable: the list lasts until reload */
  }
  bump()
}

/** Sharing is OFF until the user turns it on from the composer's + menu. */
export function captureEnabled(): boolean {
  try {
    return localStorage.getItem(ON_KEY) === '1'
  } catch {
    return false
  }
}

export function setCaptureEnabled(on: boolean) {
  try {
    localStorage.setItem(ON_KEY, on ? '1' : '0')
  } catch {
    /* storage unavailable: stays off */
  }
  bump()
}

/** Lines of the block list that cannot be read (they block every capture until fixed). */
export const invalidBlockLines = () => invalidPatterns(patterns())

const patterns = () => parsePatterns(blockText())

/** Agents that get a capture: the ones the user talks to, never an Operator. */
export const capturable = (role: string) => role === 'commander' || role.startsWith('manager-')

/** preview says what would ride along with a message to role now (null = nothing). */
// `?capture=demo` (screenshots only): pretends a web tab is showing in Electron.
// Only in a dev/test build: a production page ignores it.
const demo = !!import.meta.env?.DEV && typeof location !== 'undefined' && new URLSearchParams(location.search).get('capture') === 'demo'

export function preview(role: string): Preview | null {
  const off = !captureEnabled()
  if (demo && capturable(role)) return { host: 'app.terraform.io', title: 'Runs', blocked: isBlocked('https://app.terraform.io/', patterns()), off }
  if (!capturable(role) || !web || !bridge()?.capture) return null
  let host = web.url
  try {
    host = new URL(web.url).hostname
  } catch {
    /* shown as is */
  }
  return { host, title: web.title, blocked: isBlocked(web.url, patterns()), off }
}

export function usePreview(role: string): Preview | null {
  useSyncExternalStore(
    (cb) => {
      subs.add(cb)
      return () => subs.delete(cb)
    },
    () => version,
  )
  return preview(role)
}

const b64 = (s: string) => btoa(unescape(encodeURIComponent(s)))

/** What the user's Send press decided: which tab, under which block list, at which address. */
export type CaptureTarget = { id: number; block: string[]; expect: string }

/**
 * snapshot fixes the capture decision at the moment of Send (a held message
 * drained later must not capture whatever tab shows then). Null = capture nothing.
 */
export function snapshot(role: string): CaptureTarget | null {
  const p = preview(role)
  if (!p || p.off || p.blocked || !web) return null
  return { id: web.id, block: patterns(), expect: originPath(web.url) }
}

/** take captures the tab `target` names (default: the one showing now): files for the message and a line for the note. Null when nothing is attached (off, blocked, slow, failed). */
export async function take(role: string, target: CaptureTarget | null = snapshot(role)): Promise<{ attachments: Attachment[]; note: string } | null> {
  const b = bridge()
  // Re-checked at drain time: turned off, or not a role that gets captures, since the press.
  if (!target || !captureEnabled() || !capturable(role) || !b?.capture) return null
  const timeout = new Promise<null>((r) => setTimeout(() => r(null), CAPTURE_TIMEOUT_MS))
  try {
    const res = await Promise.race([b.capture({ id: target.id, block: target.block, expect: target.expect }), timeout])
    if (!res || !res.ok) return null
    const c = res.capture
    const where = originPath(c.url)
    const stamp = new Date().toISOString().replace(/\D/g, '').slice(0, 14)
    const attachments: Attachment[] = [{ name: `capture-${stamp}.png`, mediaType: 'image/png', dataUrl: `data:image/png;base64,${c.png}` }]
    if (c.text.trim()) attachments.push({ name: `capture-${stamp}.txt`, mediaType: 'text/plain', dataUrl: `data:text/plain;base64,${b64(c.text)}` })
    return {
      attachments,
      note: `- Focused browser tab${where ? ` (${where})` : ''}: its screenshot${c.text.trim() ? ' and page text are' : ' is'} attached to this message (capture-${stamp}.*), so you see what the user sees`,
    }
  } catch {
    return null
  }
}

export const captureHost = { preview, usePreview, snapshot, take, setEnabled: setCaptureEnabled }
