// The renderer runs in Electron (window.commanderHost from the preload) or as a
// plain page served by cmdrd at /app/ (no host: url = this origin, token from
// the one-time #token= / ?token= link, kept in sessionStorage).

import type { CommanderHost, HostConfig } from '../../shared/snapshot'
import type { ShellApi } from '../../../electron/shell-preload'

declare global {
  interface Window {
    commanderHost?: CommanderHost
    commanderShell?: ShellApi
  }
}

export const shell: ShellApi | undefined = typeof window === 'undefined' ? undefined : window.commanderShell

export const host: CommanderHost | undefined = typeof window === 'undefined' ? undefined : window.commanderHost

const tokenKey = 'commander.token'
const apiKey = 'commander.api'

/** loopbackOrigin accepts an API origin only on this machine (127.0.0.1 / localhost). */
export function loopbackOrigin(raw: string | null): string | null {
  if (!raw) return null
  try {
    const u = new URL(raw)
    if ((u.protocol === 'http:' || u.protocol === 'https:') && (u.hostname === '127.0.0.1' || u.hostname === 'localhost')) return u.origin
  } catch {
    // not a URL
  }
  return null
}

function storage(): Storage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

/** webConfig takes the token from the link (then strips it from the address bar) or sessionStorage. */
export function webConfig(loc: Location = window.location): HostConfig | null {
  const hash = new URLSearchParams(loc.hash.replace(/^#/, ''))
  const query = new URLSearchParams(loc.search)
  const fromLink = hash.get('token') || query.get('token')
  // A page served elsewhere (a dev server, the smoke run) may name a loopback API.
  const apiFromLink = loopbackOrigin(hash.get('api') || query.get('api'))
  if (fromLink) {
    storage()?.setItem(tokenKey, fromLink)
    if (apiFromLink) storage()?.setItem(apiKey, apiFromLink)
    query.delete('token')
    query.delete('api')
    const q = query.toString()
    history.replaceState(null, '', loc.pathname + (q ? `?${q}` : ''))
  }
  const token = fromLink || storage()?.getItem(tokenKey) || ''
  if (!token) return null
  return { apiUrl: apiFromLink || storage()?.getItem(apiKey) || loc.origin, token }
}

let demoConfig: HostConfig | undefined

export async function loadConfig(): Promise<HostConfig | null> {
  if (host) return host.getConfig()
  // ?fixture=chat: screenshots of the chat states without any cmdrd.
  if (new URLSearchParams(location.search).get('fixture') === 'chat') {
    if (!demoConfig) {
      let snap = (await import('../../../test/fixtures/snapshot.json')).default as unknown as HostConfig['fixture']
      // &owned=1: workstream 1 as an owned job, to see that design without cmdrd support.
      if (snap && new URLSearchParams(location.search).get('owned')) snap = (await import('./demoOwned')).withOwnedJob(snap)
      // &claim=1: a claimed step whose Operator died, behind an open PR approval gate.
      if (snap && new URLSearchParams(location.search).get('claim')) snap = (await import('./demoClaim')).withClaim(snap)
      demoConfig = { apiUrl: location.origin, token: 'fixture', fixture: snap, demo: true }
    }
    return demoConfig
  }
  return webConfig()
}

/** isMac picks the shortcut modifier label: Cmd on macOS, Ctrl elsewhere. */
export function isMac(): boolean {
  const p = host?.platform ?? navigator.platform ?? ''
  return /darwin|mac/i.test(p)
}

/** rendererSource: served by the Vite dev server (HMR, `npm run dev:renderer`) or the built bundle. */
export function rendererSource(): 'live' | 'bundled' {
  return import.meta.env.DEV ? 'live' : 'bundled'
}
