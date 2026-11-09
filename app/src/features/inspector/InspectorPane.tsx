import { OPEN_TAB_EVENT } from './connections'
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

import { CardView } from './CardView'
import { createClient, type InspectorClient } from './client'
import {
  addTabArgv,
  bodyMode,
  focusTabArgv,
  hasDigest,
  neighborAfterClose,
  normalizeUrl,
  resolveActive,
  rmTabArgv,
  syncMsg,
  cardPollMs,
} from './model'
import { bodyRect, sameRect } from './rect'
import { TabIcon, TabStrip } from './TabStrip'
import type { Card, HostWithWebview, InspectorApi, InspectorWindow, Rect, SyncMsg, TabInfo, TabList, ViewState, WebviewBridge } from './types'
import './inspector.css'

export type PinnedTab = { id: string; title: string; content: ReactNode; tone?: 'attn'; icon?: ReactNode; dot?: boolean; onClose?: () => void }

export type InspectorPaneProps = {
  api: InspectorApi
  window?: InspectorWindow // which cockpit window's tabs; omit for Commander
  bridge?: WebviewBridge | null // default: window.commanderHost?.webview (null = force web mode)
  client?: InspectorClient // tests inject one; default built from api
  renderDecision?: (tab: TabInfo) => ReactNode // the decision-card feature plugs in here
  pollMs?: number // tab list poll; default 2000
  // Built-in tabs ahead of the list that cannot be closed (the task graph, an open decision).
  // The host owns which one shows: activePinned, or null for the selected list tab.
  pinned?: PinnedTab[]
  header?: ReactNode // what the pane is focused on, above the tabs
  activePinned?: string | null
  onPinned?: (id: string | null) => void
  // Something covers the whole window (the app's start screen): web pages are
  // native views drawn above the page, so they must hide to be covered.
  covered?: boolean
  // The host's say over the list's tabs: which to show, and what to call them.
  shapeTabs?: (tabs: TabInfo[]) => TabInfo[]
  // The tabs and which one shows, whenever either changes.
  // `web` is the web tab showing right now (null when the panel shows anything else): what a message may carry a capture of.
  onTabs?: (tabs: TabInfo[], showing: string, web: { id: number; url: string; title: string } | null) => void
}

type CardEntry = { card: Card; refreshing: boolean }

const isMac = typeof navigator !== 'undefined' && /Mac/.test(navigator.platform)

export function InspectorPane(props: InspectorPaneProps) {
  const win = props.window ?? {}
  const winKey = `${win.ws ?? ''}@${win.project ?? ''}`
  const bridge = props.bridge === undefined ? (typeof window !== 'undefined' ? (window as HostWithWebview).commanderHost?.webview : undefined) : props.bridge
  const hasWebview = !!bridge
  const client = useMemo(() => props.client ?? createClient(props.api), [props.client, props.api])

  const [list, setList] = useState<TabList | null>(null)
  const [listErr, setListErr] = useState('')
  const [pending, setPending] = useState<number | null>(null)
  const [views, setViews] = useState<Record<number, ViewState>>({})
  const [cards, setCards] = useState<Record<number, CardEntry>>({})
  const [cardErr, setCardErr] = useState<Record<number, string>>({})
  const [adding, setAdding] = useState(false)
  const [addText, setAddText] = useState('')
  const [addError, setAddError] = useState('')
  const [status, setStatus] = useState('')
  const [rect, setRect] = useState<Rect | null>(null)
  const [urlDraft, setUrlDraft] = useState<string | null>(null)

  const rootRef = useRef<HTMLDivElement>(null)
  const bodyRef = useRef<HTMLDivElement>(null)
  const urlRef = useRef<HTMLInputElement>(null)
  const addRef = useRef<HTMLInputElement>(null)

  const shapeTabs = props.shapeTabs
  const tabs = useMemo(() => (shapeTabs ? shapeTabs(list?.tabs ?? []) : list?.tabs ?? []), [list, shapeTabs])
  const activeId = resolveActive(tabs, list?.active_id ?? 0, pending)
  const active = tabs.find((t) => t.id === activeId) ?? null
  const mode = active ? bodyMode(active, hasWebview) : null
  const view = active ? views[active.id] : undefined
  const showing = (props.activePinned && props.pinned?.find((p) => p.id === props.activePinned)?.title) || active?.title || 'Graph'
  const onTabs = props.onTabs
  const shownWeb = mode === 'webview' && active && !props.activePinned ? active : null
  const webUrl = shownWeb ? view?.url || shownWeb.target : ''
  const webTitle = shownWeb ? view?.title || shownWeb.title : ''
  useEffect(
    () => onTabs?.(tabs, showing, shownWeb ? { id: shownWeb.id, url: webUrl, title: webTitle } : null),
    [onTabs, tabs, showing, shownWeb?.id, webUrl, webTitle], // eslint-disable-line react-hooks/exhaustive-deps
  )
  // With no list tab to show (none yet, or the last one closed) the first
  // built-in tab shows instead: there is never an empty panel.
  const shownPinned = props.pinned?.find((t) => t.id === props.activePinned) ?? (active ? undefined : props.pinned?.[0])
  const showPinned = !!shownPinned
  // A tab you open (a link in the chat) or Commander opens for you comes to the
  // front over the built-in tab. A Manager's tab lands quietly in the strip, and
  // a close moves the active tab too: neither takes the panel from you.
  const lastList = useRef<TabList | null>(null)
  useEffect(() => {
    const prev = lastList.current
    lastList.current = list
    if (!prev || !list || list.active_id === prev.active_id || !list.active_id) return
    const closed = !list.tabs.some((t) => t.id === prev.active_id)
    const by = list.tabs.find((t) => t.id === list.active_id)?.added_by ?? ''
    if (!closed && !by.startsWith('manager-') && !by.startsWith('operator-')) props.onPinned?.(null)
  }, [list]) // eslint-disable-line react-hooks/exhaustive-deps

  // ---- tab list: poll + focus events ---------------------------------------
  const refreshList = useCallback(async () => {
    try {
      const next = await client.listTabs(win)
      setList(next)
      setListErr('')
      setPending((p) => (p !== null && next.active_id === p ? null : p))
    } catch (e) {
      setListErr(String((e as Error).message ?? e))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, winKey])

  useEffect(() => {
    setList(null)
    setPending(null)
    void refreshList()
    const t = setInterval(refreshList, props.pollMs ?? 2000)
    const onFocus = () => void refreshList()
    const onVis = () => document.visibilityState === 'visible' && void refreshList()
    window.addEventListener('focus', onFocus)
    document.addEventListener('visibilitychange', onVis)
    return () => {
      clearInterval(t)
      window.removeEventListener('focus', onFocus)
      document.removeEventListener('visibilitychange', onVis)
    }
  }, [refreshList, props.pollMs])

  // ---- cards for text tabs (and digests in web mode) ------------------------
  const wantsCard = !!active && (mode === 'card' || (mode === 'link' && hasDigest(active)))
  const loadCard = useCallback(
    async (id: number, force = false) => {
      try {
        const res = await client.card(id, force)
        setCards((c) => ({ ...c, [id]: { card: res.card, refreshing: res.refreshing } }))
        setCardErr((e) => ({ ...e, [id]: '' }))
        return res.refreshing
      } catch (e) {
        setCardErr((x) => ({ ...x, [id]: String((e as Error).message ?? e) }))
        return false
      }
    },
    [client],
  )
  useEffect(() => {
    if (!wantsCard || !active) return
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      const refreshing = await loadCard(active.id)
      if (!stop) timer = setTimeout(tick, cardPollMs(active, refreshing))
    }
    void tick()
    return () => {
      stop = true
      clearTimeout(timer)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantsCard, active?.id, active?.refresh_secs, loadCard])

  // ---- native views (Electron only) -----------------------------------------
  useEffect(() => {
    if (!bridge) return
    const offState = bridge.onState((s) => setViews((v) => ({ ...v, [s.id]: s })))
    const offShortcut = bridge.onShortcut(() => {
      setUrlDraft(null)
      setTimeout(() => urlRef.current?.select(), 0)
    })
    return () => {
      offState()
      offShortcut()
    }
  }, [bridge])

  // Measure the body after every render: its size (ResizeObserver) and its
  // position (a toolbar appearing shifts it) both move the native view.
  const measure = useCallback(() => {
    const el = bodyRef.current
    const next = el ? bodyRect(el.getBoundingClientRect()) : null
    setRect((r) => (sameRect(r, next) ? r : next))
  }, [])
  useLayoutEffect(measure)
  useEffect(() => {
    const el = bodyRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    window.addEventListener('resize', measure)
    // A move with no resize (styles arriving after a restart push the body
    // down) fires neither, so check again each second.
    const poll = window.setInterval(measure, 1000)
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', measure)
      window.clearInterval(poll)
    }
  }, [measure])

  const lastSync = useRef<SyncMsg | null>(null)
  const hideViews = showPinned || adding || !!props.covered // the pinned tab, or the start screen, draws over where a page would be
  useEffect(() => {
    if (!bridge || list === null) return
    const msg = syncMsg(tabs, activeId, rect, hasWebview, hideViews)
    if (JSON.stringify(msg) === JSON.stringify(lastSync.current)) return
    lastSync.current = msg
    bridge.sync(msg)
  })
  useEffect(() => {
    // Unmount or window switch: hide the views but keep them alive (a tab
    // list without them is what destroys a view).
    return () => {
      const last = lastSync.current
      if (bridge && last) bridge.sync({ ...last, activeId: null, rect: null })
      lastSync.current = null
    }
  }, [bridge, winKey])

  // ---- actions ----------------------------------------------------------------
  const run = useCallback(
    async (argv: string[]) => {
      try {
        await client.cmd(argv)
        setStatus('')
      } catch (e) {
        setStatus(String((e as Error).message ?? e))
      }
      await refreshList()
    },
    [client, refreshList],
  )

  const select = (id: number) => {
    props.onPinned?.(null)
    if (id === activeId) return
    setPending(id)
    void run(focusTabArgv(win, id))
  }
  const close = (id: number) => {
    if (id === activeId) setPending(neighborAfterClose(tabs, id))
    void run(rmTabArgv(win, id))
  }
  const addUrl = (url: string, title?: string) => {
    setPending(null)
    void run(addTabArgv(win, url, title))
  }
  // Elsewhere in the app (the sidebar's Connections) can open a page here.
  const addUrlRef = useRef(addUrl)
  addUrlRef.current = addUrl
  useEffect(() => {
    const open = (e: Event) => {
      e.preventDefault() // claimed: openInPanel leaves the browser alone
      const { url, title } = (e as CustomEvent<{ url: string; title?: string }>).detail
      addUrlRef.current(url, title)
    }
    window.addEventListener(OPEN_TAB_EVENT, open)
    return () => window.removeEventListener(OPEN_TAB_EVENT, open)
  }, [])
  useEffect(() => {
    if (adding) addRef.current?.focus()
  }, [adding])
  const submitAdd = () => {
    const url = normalizeUrl(addText)
    if (!url) {
      setAddError('Not a web address')
      return
    }
    setAdding(false)
    setAddText('')
    setAddError('')
    addUrl(url)
  }
  const openExternal = (url: string) => {
    if (bridge) bridge.openExternal(url)
    else window.open(url, '_blank', 'noopener,noreferrer')
  }
  const reload = () => {
    if (!active) return
    if (mode === 'webview') bridge?.nav({ id: active.id, action: 'reload' })
    else if (wantsCard) {
      setCards((c) => (c[active.id] ? { ...c, [active.id]: { ...c[active.id], refreshing: true } } : c))
      void loadCard(active.id, true)
    }
  }

  const onKeyDown = (e: KeyboardEvent) => {
    const mod = isMac ? e.metaKey : e.ctrlKey
    const inField = (e.target as HTMLElement).tagName === 'INPUT'
    if (e.key === 'Escape') {
      setAdding(false)
      return
    }
    if (mod && e.key.toLowerCase() === 'l' && mode === 'webview') {
      e.preventDefault()
      urlRef.current?.select()
    } else if (mod && e.key.toLowerCase() === 'r') {
      e.preventDefault()
      reload()
    } else if (mod && (e.key === '[' || e.key === ']') && mode === 'webview' && active) {
      e.preventDefault()
      bridge?.nav({ id: active.id, action: e.key === '[' ? 'back' : 'forward' })
    } else if (!mod && !inField && e.key === 'a') {
      e.preventDefault()
      setAdding(true)
    } else if (!mod && !inField && /^[1-9]$/.test(e.key) && tabs[Number(e.key) - 1]) {
      select(tabs[Number(e.key) - 1].id)
    }
  }

  // ---- render -------------------------------------------------------------------
  const body = (): ReactNode => {
    if (list === null) {
      return <div className="insp-empty">{listErr ? <span className="insp-bad">{listErr}</span> : 'Loading tabs…'}</div>
    }
    if (!active) {
      return (
        <div className="insp-empty">
          <div className="insp-empty-title">Nothing open</div>
          <div>Open a pitch from Needs you, or a link from the chat. Press <kbd>a</kbd> to add a URL.</div>
          <button className="insp-btn" onClick={() => setAdding(true)}>
            Add a URL
          </button>
        </div>
      )
    }
    const entry = cards[active.id]
    switch (mode) {
      case 'decision':
        return props.renderDecision?.(active) ?? <div className="insp-empty">Decision {active.target}: answer it in the decision card.</div>
      case 'card':
        return <CardView card={entry?.card ?? null} loading={!entry || entry.refreshing} error={cardErr[active.id]} onOpen={openExternal} />
      case 'link':
        return (
          <div className="insp-linkcard">
            <div className="insp-linkcard-head">
              <TabIcon tab={active} />
              <div className="insp-linkcard-text">
                <div className="insp-card-title">{active.title}</div>
                <div className="insp-muted insp-ellipsis">{active.target}</div>
              </div>
              <button className="insp-btn" onClick={() => openExternal(active.target)}>
                Open in browser
              </button>
            </div>
            <div className="insp-muted insp-note">Live pages open in the desktop app; this is the web view of the cockpit.</div>
            {hasDigest(active) && <CardView card={entry ? { ...entry.card, URL: '' } : null} loading={!entry || entry.refreshing} error={cardErr[active.id]} onOpen={openExternal} />}
          </div>
        )
      case 'webview':
        if (view?.error) {
          return (
            <div className="insp-empty insp-overlay">
              <div className="insp-empty-title">Couldn’t load this page</div>
              <div className="insp-bad">{view.error}</div>
              <div className="insp-row">
                <button className="insp-btn" onClick={reload}>
                  Retry
                </button>
                <button className="insp-btn insp-btn-quiet" onClick={() => openExternal(view.url || active.target)}>
                  Open in browser
                </button>
              </div>
            </div>
          )
        }
        // The native view covers this; it shows only before the view paints.
        return <div className="insp-empty insp-muted">Loading {active.title}…</div>
    }
    return null
  }

  return (
    <div className="insp" ref={rootRef} onKeyDown={onKeyDown} tabIndex={-1}>
      {props.header}
      <TabStrip
        tabs={tabs}
        activeId={showPinned || adding ? null : activeId}
        pinned={props.pinned?.map((t) => ({ id: t.id, title: t.title, tone: t.tone, icon: t.icon, active: !adding && t.id === shownPinned?.id, onClose: t.onClose }))}
        onPinned={(id) => {
          setAdding(false)
          props.onPinned?.(id)
        }}
        views={views}
        cards={cards}
        adding={adding}
        addText={addText}
        addError={addError}
        onSelect={(id) => {
          setAdding(false)
          select(id)
        }}
        onClose={close}
        onStartAdd={() => setAdding(true)}
        onAddText={(s) => {
          setAddText(s)
          setAddError('')
        }}
        onAddSubmit={submitAdd}
        onAddCancel={() => {
          setAdding(false)
          setAddText('')
          setAddError('')
        }}
      />
      {adding && (
        <div className="insp-toolbar">
          <form
            className="insp-url"
            onSubmit={(e) => {
              e.preventDefault()
              submitAdd()
            }}
          >
            <input
              ref={addRef}
              className={'insp-url-input' + (addError ? ' insp-add-bad' : '')}
              placeholder="Enter a web address"
              value={addText}
              title={addError}
              onChange={(e) => {
                setAddText(e.target.value)
                setAddError('')
              }}
              spellCheck={false}
            />
          </form>
        </div>
      )}
      {!adding && !showPinned && mode === 'webview' && active && (
        <div className="insp-toolbar">
          <form
            className="insp-url"
            onSubmit={(e) => {
              e.preventDefault()
              const url = normalizeUrl(urlDraft ?? '')
              if (url) bridge?.nav({ id: active.id, action: 'load', url })
              setUrlDraft(null)
              urlRef.current?.blur()
            }}
          >
            <input
              ref={urlRef}
              className="insp-url-input"
              value={urlDraft ?? view?.url ?? active.target}
              onChange={(e) => setUrlDraft(e.target.value)}
              onFocus={(e) => {
                setUrlDraft(e.target.value)
                e.target.select()
              }}
              onBlur={() => setUrlDraft(null)}
              spellCheck={false}
            />
          </form>
          {view?.loading && <div className="insp-progress" />}
        </div>
      )}
      <div className="insp-body" ref={bodyRef}>
        {adding ? null : shownPinned ? shownPinned.content : body()}
      </div>
      {(status || (listErr && list)) && <div className="insp-status insp-bad">{status || listErr}</div>}
    </div>
  )
}
