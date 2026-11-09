import { useEffect, useRef, useState, type ReactNode } from 'react'

import { stateDot, tabIcon, type Dot } from './model'
import type { Card, TabInfo, ViewState } from './types'

type Props = {
  tabs: TabInfo[]
  activeId: number | null
  views: Record<number, ViewState>
  cards: Record<number, { card: Card; refreshing: boolean }>
  adding: boolean
  addText: string
  addError: string
  onSelect: (id: number) => void
  onClose: (id: number) => void
  onStartAdd: () => void
  onAddText: (s: string) => void
  onAddSubmit: () => void
  onAddCancel: () => void
  pinned?: { id: string; title: string; active: boolean; tone?: 'attn'; icon?: ReactNode; dot?: boolean; onClose?: () => void }[] // built in, always first; closable only with onClose (a Decision)
  onPinned?: (id: string) => void
}

export function TabIcon({ tab }: { tab: TabInfo }) {
  const ico = tabIcon(tab)
  return <span className={`insp-ico insp-ico-${ico.site}`}>{ico.label}</span>
}

function DotMark({ dot }: { dot: Dot }) {
  if (dot === 'none') return null
  return <span className={`insp-dot insp-dot-${dot}`} aria-label={dot} />
}

// A web page's own title and icon, remembered by URL: a tab shows them before
// its page loads again (pages load only when their tab is opened).
const META_KEY = 'insp.pageMeta'
type PageMeta = Record<string, { title?: string; favicon?: string }>
function readMeta(): PageMeta {
  try {
    return JSON.parse(localStorage.getItem(META_KEY) || '{}') as PageMeta
  } catch {
    return {}
  }
}

export function TabStrip(p: Props) {
  const [meta, setMeta] = useState<PageMeta>(readMeta)
  useEffect(() => {
    let changed = false
    const next = { ...meta }
    for (const t of p.tabs) {
      const v = p.views[t.id]
      if (!v || (!v.title && !v.favicon)) continue
      const m = { title: v.title || next[t.target]?.title, favicon: v.favicon || next[t.target]?.favicon }
      if (m.title !== next[t.target]?.title || m.favicon !== next[t.target]?.favicon) {
        next[t.target] = m
        changed = true
      }
    }
    if (!changed) return
    setMeta(next)
    try {
      localStorage.setItem(META_KEY, JSON.stringify(next))
    } catch {
      // only this session remembers
    }
  }, [p.tabs, p.views, meta])
  const stripRef = useRef<HTMLDivElement>(null)

  // Keep the active tab scrolled into view.
  useEffect(() => {
    const el = stripRef.current?.querySelector<HTMLElement>('[data-active="true"]')
    el?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [p.activeId, p.tabs.length])

  return (
    <div className="insp-strip">
      <div className="insp-tabs" ref={stripRef} role="tablist">
        {p.pinned?.map((t) => (
          <div
            key={t.id}
            role="tab"
            aria-selected={t.active}
            data-active={t.active}
            className={'insp-tab insp-tab-pinned' + (t.tone ? ` insp-tab-${t.tone}` : '') + (t.active ? ' insp-tab-active' : '')}
            onClick={() => p.onPinned?.(t.id)}
          >
            {t.icon}
            <span className="insp-tab-title">{t.title}</span>
            {t.dot && <DotMark dot="attn" />}
            {t.onClose && (
              <button
                className="insp-x"
                aria-label={`Close ${t.title}`}
                onClick={(e) => {
                  e.stopPropagation()
                  t.onClose?.()
                }}
              >
                ×
              </button>
            )}
          </div>
        ))}
        {p.tabs.map((t, i) => {
          const active = t.id === p.activeId
          return (
            <div
              key={t.id}
              role="tab"
              aria-selected={active}
              data-active={active}
              className={'insp-tab' + (active ? ' insp-tab-active' : '')}
              title={t.target}
              onMouseDown={(e) => {
                if (e.button === 1) {
                  e.preventDefault()
                  p.onClose(t.id)
                }
              }}
              onClick={() => p.onSelect(t.id)}
            >
              {(p.views[t.id]?.favicon || meta[t.target]?.favicon) ? <img className="insp-favicon" src={p.views[t.id]?.favicon || meta[t.target]?.favicon} alt="" /> : <TabIcon tab={t} />}
              <span className="insp-tab-title">{p.views[t.id]?.title || meta[t.target]?.title || t.title}</span>
              <DotMark dot={stateDot(t, p.views[t.id], p.cards[t.id])} />
              {i < 9 && <span className="insp-tab-num">{i + 1}</span>}
              <button
                className="insp-x"
                aria-label={`Close ${t.title}`}
                onClick={(e) => {
                  e.stopPropagation()
                  p.onClose(t.id)
                }}
              >
                ×
              </button>
            </div>
          )
        })}
        {p.adding && (
          <div role="tab" aria-selected data-active="true" className="insp-tab insp-tab-active">
            <span className="insp-tab-title">New tab</span>
            <button
              className="insp-x"
              aria-label="Close New tab"
              onClick={(e) => {
                e.stopPropagation()
                p.onAddCancel()
              }}
            >
              ×
            </button>
          </div>
        )}
      </div>
      <button className="insp-iconbtn" title="New tab (a)" aria-label="New tab" onClick={p.onStartAdd}>
        +
      </button>
    </div>
  )
}
