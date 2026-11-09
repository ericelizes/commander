import type { CardResponse, CmdResult, InspectorApi, InspectorWindow, TabList } from './types'

export type InspectorClient = {
  listTabs(win: InspectorWindow): Promise<TabList>
  card(id: number, force?: boolean): Promise<CardResponse>
  cmd(argv: string[]): Promise<CmdResult>
}

export function createClient(api: InspectorApi): InspectorClient {
  const doFetch = api.fetch ?? fetch.bind(globalThis)
  const headers = { Authorization: `Bearer ${api.token}` }
  const getJSON = async <T>(path: string): Promise<T> => {
    const res = await doFetch(api.baseUrl + path, { headers })
    const body = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(body?.error || `${path}: HTTP ${res.status}`)
    return body as T
  }
  return {
    listTabs(win) {
      const q = new URLSearchParams()
      if (win.ws) q.set('ws', win.ws)
      if (win.project) q.set('project', win.project)
      const qs = q.toString()
      return getJSON<TabList>('/api/tabs' + (qs ? '?' + qs : ''))
    },
    card(id, force = false) {
      return getJSON<CardResponse>(`/api/tabs/card?id=${id}${force ? '&force=1' : ''}`)
    },
    async cmd(argv) {
      const res = await doFetch(api.baseUrl + '/api/cmd', {
        method: 'POST',
        headers: { ...headers, 'Content-Type': 'application/json' },
        body: JSON.stringify({ argv }),
      })
      const body = (await res.json().catch(() => ({}))) as CmdResult
      if (!res.ok || !body.ok) throw new Error(body.error || body.stderr?.trim() || `tab command failed (${res.status})`)
      return body
    },
  }
}
