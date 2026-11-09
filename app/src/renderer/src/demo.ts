// Screenshot fixture for the inspector: `?demo=web` (link cards, as a plain
// page shows url tabs) or `?demo=electron` (a stand-in for the native webview
// drawn at the rect the pane syncs). Only the inspector's tab list is faked;
// the navigator and decision cards stay on the real cmdrd.

import type { InspectorClient, WebviewBridge } from '../../features/inspector'
import { useStore } from './store'
import { askKey, wsKey } from './viewmodel'

type Demo = 'web' | 'electron'

export function demoMode(): Demo | null {
  if (typeof location === 'undefined') return null
  const v = new URLSearchParams(location.search).get('demo')
  return v === 'web' || v === 'electron' ? v : null
}

const tab = (id: number, title: string, target: string, detected: string, view: 'web' | 'card', kind = 'url') => ({
  id,
  position: id,
  title,
  kind,
  target,
  renderer: view === 'card' ? detected : '',
  detected,
  view,
  refresh_secs: 60,
  added_by: 'manager',
  created_at: 0,
})

const tabs = [
  tab(1, 'Job workers design', 'https://www.notion.so/acme/Job-workers-design-8f2c', 'notion', 'web'),
  tab(2, 'PR #105', 'https://github.com/acme/example-app/pull/105', 'github-pr', 'card'),
  tab(3, 'go test', 'go test ./...', 'cmd', 'card', 'cmd'),
]

const fetched = new Date(Date.now() - 3 * 60_000).toISOString()
const card = (Title: string, extra: object) => ({
  Renderer: 'github-pr',
  Title,
  State: '',
  URL: '',
  Meta: null,
  Sections: null,
  Markdown: '',
  Preformatted: false,
  Attn: false,
  UpdatedAt: '',
  FetchedAt: fetched,
  Err: '',
  ...extra,
})

const cards: Record<number, ReturnType<typeof card>> = {
  1: card('Job workers design', { Renderer: 'notion', URL: tabs[0].target, Meta: ['edited by the user', '18 blocks'], Markdown: '## Goal\n\nMove background jobs to **dedicated** workers.\n\n- Retry policy per activity\n- One worker pool per app' }),
  2: card('PR #105  widget-lib 2.4.1', {
    State: 'OPEN',
    URL: tabs[1].target,
    Meta: ['deps/widget-lib → main', '+20 −20', '10 files'],
    Sections: [{ Title: 'CHECKS', Lines: ['✓ 1  ✗ 4  ○ 0', '✗ pytest (HTTP 451 from the package mirror)'] }],
    Attn: true,
    Markdown: 'Bumps widget-lib to 2.4.1 in every lockfile.',
  }),
  3: card('go test', { Renderer: 'cmd', Meta: ['$ go test ./...'], Preformatted: true, State: 'PASSED', Markdown: '\x1b[32mok  \x1b[0m\tinternal/livedata\t0.41s\n\x1b[32mok  \x1b[0m\tinternal/api\t1.09s' }),
}

export function demoClient(_mode: Demo): InspectorClient {
  let active = 1
  return {
    listTabs: async () => ({ project_id: 1, ws: 'demo', active_id: active, tabs }),
    card: async (id) => ({ tab_id: id, card: cards[id], stale: false, refreshing: false }),
    cmd: async (argv) => {
      if (argv[1] === 'focus') active = Number(String(argv[argv.length - 1]).replace('#', '')) || active
      return { ok: true, stdout: '', stderr: '', exit: 0 }
    },
  }
}

/** demoBridge draws a white "page" where Electron would place the native view. */
export function demoBridge(): WebviewBridge {
  const el = document.createElement('div')
  el.style.cssText =
    'position:fixed;display:none;background:#fff;color:#37352f;font:14px/1.55 -apple-system,system-ui,sans-serif;overflow:hidden;z-index:5;box-sizing:border-box;padding:28px 32px'
  el.innerHTML =
    '<div style="font-size:30px;font-weight:700;margin:8px 0 14px">Background job workers</div><div style="color:#787774;margin-bottom:18px">Engineering · Design doc</div><p>Move background jobs to dedicated workers. Each activity gets its own retry policy; one worker pool per app.</p><p style="color:#787774">(stand-in for the logged-in Notion page that Electron shows here)</p>'
  document.body.appendChild(el)
  return {
    sync(msg) {
      const t = tabs.find((x) => x.id === msg.activeId)
      if (!msg.rect || !t || t.view !== 'web') {
        el.style.display = 'none'
        return
      }
      Object.assign(el.style, { display: 'block', left: `${msg.rect.x}px`, top: `${msg.rect.y}px`, width: `${msg.rect.width}px`, height: `${msg.rect.height}px` })
    },
    nav: () => {},
    openExternal: () => {},
    signinStatus: async () => ({}),
    onState: () => () => {},
    onShortcut: () => () => {},
  }
}

/**
 * demoSelect applies `?ask=n` (open the nth NEEDS YOU card) or `?ws=n` (go to
 * workstream n; with `flow=1`, its workflow tab) once the first snapshot arrives, for screenshots.
 */
export function demoSelect(): void {
  if (typeof location === 'undefined') return
  const q = new URLSearchParams(location.search)
  const ask = Number(q.get('ask') || 0)
  const ws = Number(q.get('ws') || 0)
  if (!ask && !ws) return
  const stop = useStore.subscribe((st) => {
    if (!st.snapshot) return
    stop()
    const a = st.snapshot.needs_you[ask - 1]
    const w = st.snapshot.workstreams[ws - 1]
    if (a) st.select({ kind: 'ask', key: askKey(a) })
    else if (w) {
      st.goWorkstream(wsKey(w))
      if (q.get('flow')) st.openWorkflow(wsKey(w))
      if (q.get('agent') === 'manager' && q.get('fixture') === 'chat') st.select({ kind: 'agent', key: w.manager_role, ws: wsKey(w) })
    }
  })
}
