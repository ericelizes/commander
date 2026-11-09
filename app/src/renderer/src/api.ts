// The features' views of cmdrd, built from the store's config (URL + token).
// Memoized on the config so components keyed on api identity don't refetch.

import { useMemo } from 'react'
import type { CmdResult, CommanderApi, SnapshotLike } from '../../features/decisions'
import type { InspectorApi } from '../../features/inspector'
import type { HostConfig } from '../../shared/snapshot'
import { host } from './host'
import { useStore } from './store'

export function commanderApi(cfg: HostConfig): CommanderApi {
  const headers = { Authorization: `Bearer ${cfg.token}` }
  return {
    async snapshot() {
      const res = await fetch(`${cfg.apiUrl}/api/snapshot`, { headers })
      if (!res.ok) throw new Error(`snapshot: HTTP ${res.status}`)
      return (await res.json()) as SnapshotLike
    },
    async runCmd(argv) {
      const res = await fetch(`${cfg.apiUrl}/api/cmd`, {
        method: 'POST',
        headers: { ...headers, 'Content-Type': 'application/json' },
        body: JSON.stringify({ argv }),
      })
      const body = (await res.json().catch(() => null)) as CmdResult | null
      return body ?? { ok: false, stdout: '', stderr: '', exit: -1, error: `HTTP ${res.status}` }
    },
    // Electron: the inspector bridge opens links in the default browser.
    openUrl: (url) => {
      if (host?.webview) host.webview.openExternal(url)
      else window.open(url, '_blank', 'noopener,noreferrer')
    },
  }
}

export function useApis(): { cmdr: CommanderApi; insp: InspectorApi } | null {
  const cfg = useStore((s) => s.config)
  return useMemo(() => (cfg ? { cmdr: commanderApi(cfg), insp: { baseUrl: cfg.apiUrl, token: cfg.token } } : null), [cfg])
}
