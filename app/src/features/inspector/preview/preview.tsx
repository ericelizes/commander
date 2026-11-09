// Dev preview of <InspectorPane> with a fake cmdrd and (in ?mode=electron) a
// fake bridge that draws a stand-in for the native WebContentsView at the
// rect the pane sends, so the geometry can be checked in any browser,
// including chrome-headless-shell. ?mode=web shows the web-mode fallback.

import { createRoot } from 'react-dom/client'

import type { InspectorClient } from '../client'
import { InspectorPane } from '../InspectorPane'
import type { Card, SyncMsg, TabInfo, TabList, ViewState, WebviewBridge } from '../types'

const params = new URLSearchParams(location.search)
const electron = params.get('mode') !== 'web'
// ?bridge=host&api=http://127.0.0.1:port&token=t: the real bridge and a real
// (or fake) cmdrd, used by the Electron smoke test.
const hostMode = params.get('bridge') === 'host'
const activeParam = Number(params.get('active') || 1)

const tab = (id: number, title: string, target: string, over: Partial<TabInfo> = {}): TabInfo => ({
  id,
  position: id,
  title,
  kind: 'url',
  target,
  renderer: '',
  detected: 'web',
  view: 'web',
  refresh_secs: 60,
  added_by: 'manager-bd-12',
  created_at: 0,
  ...over,
})

let list: TabList = {
  project_id: 1,
  ws: 'bd-12',
  active_id: activeParam,
  tabs: [
    tab(1, 'Billing spec', 'https://www.notion.so/acme/Billing-spec-8f2c', { detected: 'notion' }),
    tab(2, 'PR #42', 'https://github.com/acme/api/pull/42', { renderer: 'github-pr', detected: 'github-pr', view: 'card' }),
    tab(3, 'go test', 'go test ./...', { kind: 'cmd', detected: 'cmd', view: 'card', refresh_secs: 10 }),
    tab(4, 'API latency', 'https://app.datadoghq.com/dashboard/abc-123', { detected: 'link' }),
    tab(5, 'ENG-311', 'https://linear.app/acme/issue/ENG-311', { detected: 'linear' }),
  ],
}

const now = new Date(Date.now() - 4 * 60_000).toISOString()
const base = { State: '', URL: '', Meta: null, Sections: null, Preformatted: false, Attn: false, UpdatedAt: '', FetchedAt: now, Err: '' }
const cards: Record<number, Card> = {
  1: {
    ...base,
    Renderer: 'notion',
    Title: 'Billing spec',
    URL: list.tabs[0].target,
    Meta: ['edited by Dana', '12 blocks'],
    Markdown: '## Goal\n\nMove invoices to the **usage-based** plan by Q1.\n\n## Open questions\n\n- Proration for mid-cycle upgrades\n- Tax handling in `EU` regions\n\n> Decision needed from the user on grandfathering.',
  },
  2: {
    ...base,
    Renderer: 'github-pr',
    Title: 'PR #42  Usage-based invoice generator',
    State: 'OPEN',
    URL: list.tabs[1].target,
    Meta: ['bd-12/invoices → main', '+412 −88', '9 files', 'by operator-bd-12.3'],
    Sections: [
      { Title: 'CHECKS', Lines: ['✓ 7  ✗ 1  ○ 0', '✗ integration (postgres)'] },
      { Title: 'REVIEW', Lines: ['⚑ awaiting review'] },
    ],
    Attn: true,
    Markdown: 'Adds `InvoiceBuilder` for metered plans.\n\n| plan | before | after |\n|---|---|---|\n| team | flat | metered |\n\nCloses ENG-311.',
  },
  3: {
    ...base,
    Renderer: 'cmd',
    Title: 'go test',
    Meta: ['$ go test ./...'],
    Preformatted: true,
    Markdown:
      '\x1b[32mok  \x1b[0m\tgithub.com/acme/api/internal/billing\t0.412s\n\x1b[32mok  \x1b[0m\tgithub.com/acme/api/internal/store\t1.093s\n\x1b[31m--- FAIL: TestProration (0.00s)\x1b[0m\n    proration_test.go:41: got 1999, want 2000\n\x1b[1;31mFAIL\x1b[0m\tgithub.com/acme/api/internal/invoice\t0.201s',
    State: 'FAILED',
    Attn: true,
  },
  5: {
    ...base,
    Renderer: 'linear',
    Title: 'ENG-311 Usage-based billing',
    State: 'IN PROGRESS',
    URL: list.tabs[4].target,
    Meta: ['assignee the user', 'priority High'],
    Markdown: 'Ship metered invoices behind `billing.usage` flag.\n\n1. Generator (PR #42)\n2. Proration\n3. Backfill',
  },
}

const client: InspectorClient = {
  listTabs: async () => list,
  card: async (id) => ({ tab_id: id, card: cards[id] ?? { ...base, Renderer: 'link', Title: 'link', Markdown: '' }, stale: false, refreshing: false }),
  cmd: async (argv) => {
    const id = Number((argv[2] ?? '').replace('#', ''))
    if (argv[1] === 'focus') list = { ...list, active_id: id }
    if (argv[1] === 'rm') list = { ...list, tabs: list.tabs.filter((t) => t.id !== id) }
    return { ok: true, stdout: '', stderr: '', exit: 0 }
  },
}

// The stand-in "native view": an absolutely positioned page at the synced rect.
function fakeBridge(): WebviewBridge {
  const el = document.createElement('div')
  el.id = 'fake-native-view'
  el.style.cssText = 'position:fixed;display:none;background:#fff;color:#1f2328;font:14px/1.5 -apple-system,system-ui,sans-serif;overflow:hidden;z-index:10;'
  document.body.appendChild(el)
  let stateCb: ((s: ViewState) => void) | null = null
  return {
    sync(msg: SyncMsg) {
      ;(window as unknown as { lastSync: SyncMsg }).lastSync = msg
      if (!msg.rect || msg.activeId === null) {
        el.style.display = 'none'
        return
      }
      const r = msg.rect
      const t = list.tabs.find((x) => x.id === msg.activeId)!
      Object.assign(el.style, { display: 'block', left: r.x + 'px', top: r.y + 'px', width: r.width + 'px', height: r.height + 'px' })
      el.innerHTML = `<div style="padding:28px 36px"><div style="font-size:12px;color:#6e7781">WebContentsView #${t.id} at ${r.x},${r.y} ${r.width}×${r.height}</div>
        <h1 style="font-size:28px;margin:8px 0 4px">${t.title}</h1><div style="color:#6e7781;margin-bottom:18px">${t.target}</div>
        <p>This rectangle stands in for the live, signed-in page that Electron lays over the inspector body.</p>
        <div style="height:120px;border-radius:8px;background:repeating-linear-gradient(45deg,#f6f8fa,#f6f8fa 10px,#eaeef2 10px,#eaeef2 20px)"></div></div>`
      setTimeout(() => stateCb?.({ id: t.id, url: t.target, title: t.title, loading: false, error: null, canGoBack: true, canGoForward: false }), 0)
    },
    nav() {},
    openExternal() {},
    signinStatus: async () => ({ notion: true, github: true }),
    onState(cb) {
      stateCb = cb
      return () => (stateCb = null)
    },
    onShortcut: () => () => {},
  }
}

const previewBridge = electron && !hostMode ? fakeBridge() : null

function Shell() {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '220px 1fr 520px', height: '100vh', background: '#11111b' }}>
      <div style={{ background: '#181825', borderRight: '1px solid #313244', color: '#7f849c', font: '12px system-ui', padding: 14 }}>navigator</div>
      <div style={{ background: '#1e1e2e', color: '#7f849c', font: '12px system-ui', padding: 14 }}>chat</div>
      <div style={{ borderLeft: '1px solid #313244', minWidth: 0 }}>
        {hostMode ? (
          <InspectorPane api={{ baseUrl: params.get('api') ?? '', token: params.get('token') ?? '' }} />
        ) : (
          <InspectorPane api={{ baseUrl: '', token: '' }} client={client} bridge={previewBridge} window={{ ws: 'bd-12', project: 'acme' }} />
        )}
      </div>
    </div>
  )
}

document.body.style.margin = '0'
createRoot(document.getElementById('root')!).render(<Shell />)
