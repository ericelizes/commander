import { ActivityFreshness } from './components/ActivityFreshness'
import { useLinearSprint } from '../../features/linear/sprint'
import { useCallback, useEffect, useState } from 'react'
import { connect, useStore } from './store'
import { host, isMac } from './host'
import { Navigator } from './components/Navigator'
import { Rail } from './components/Rail'
import { TopBar } from './components/TopBar'
import { Splitter } from './components/Splitter'
import { InspectorColumn } from './components/InspectorColumn'
import { ProgressView } from './components/ProgressView'
import { WorkflowsView } from './components/WorkflowsView'
import { isPage } from './viewmodel'
import { ChatSlot, useChatTarget } from './components/ChatSlot'
import { useAppCapture } from './appCapture'
import { useShell } from './shell'
import { HEADER_SLOT_ID } from '../../features/chat/Header'
import { applyTheme, loadTheme } from './theme'
import { StartScreen } from './components/StartScreen'

const RAIL = 56
const NAV = { def: 340, min: 260, max: 520 }
// The chat keeps one width, focused or not, so its input never changes size;
// the focus panel takes what is left between the sidebar and the chat.
const CHAT = { def: 640, min: 460, max: 1400 }
const PANEL_MIN = 300
// The workflow's lanes need this much beside the chat: the chat stops growing
// there, and a window that cannot give it even to the narrowest chat makes the
// chat a tab instead.
const PANE_SPLIT_MIN = 600
const CHAT_COL_MAX = 760
const MIDDLE_MIN = CHAT.min
// v3: the chat's width is the one saved; the focus panel flexes.
const sizesKey = 'commander.columns.v3'

function loadSizes(): { nav: number; chat: number } {
  try {
    const raw = JSON.parse(localStorage.getItem(sizesKey) ?? 'null')
    if (raw && typeof raw.nav === 'number' && typeof raw.chat === 'number') return raw
  } catch {
    // no storage: defaults
  }
  return { nav: NAV.def, chat: CHAT.def }
}

function clamp(v: number, lo: number, hi: number) {
  return Math.min(Math.max(v, lo), Math.max(lo, hi))
}

export function App() {
  const [sizes, setSizes] = useState(loadSizes)
  const [winW, setWinW] = useState(window.innerWidth)
  const conn = useStore((s) => s.conn)
  const snapshot = useStore((s) => s.snapshot)
  const stateUnavailable = !!snapshot?.degraded
  const platform = isMac() ? 'mac' : 'other'
  useShell()
  useLinearSprint()
  useAppCapture()
  useTheme()

  useEffect(() => {
    const stop = connect((ok, detail) => {
      // Two frames: the snapshot is painted before the host screenshots it.
      requestAnimationFrame(() =>
        requestAnimationFrame(() => host?.rendered({ ok, detail })),
      )
    })
    const unShortcut = host?.onShortcut((n) => useStore.getState().pressShortcut(n))
    const onResize = () => setWinW(window.innerWidth)
    window.addEventListener('resize', onResize)
    return () => {
      stop()
      unShortcut?.()
      window.removeEventListener('resize', onResize)
    }
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented) return
      // Cmd/Ctrl+[ and ] go Back and Forward, from anywhere (even while typing).
      if ((e.metaKey || e.ctrlKey) && (e.key === '[' || e.key === ']')) {
        e.preventDefault()
        if (e.key === '[') useStore.getState().goBack()
        else useStore.getState().goForward()
        return
      }
      const t = e.target as HTMLElement | null
      if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName))) return
      // The inspector, decision card, NEEDS YOU queue and chat own their keys.
      if (t?.closest?.('.insp, .cmdr-dec, .cmdr-nyq, .cmdr-html, .cmdr-chat, .modal')) return
      const st = useStore.getState()
      // In a plain browser there is no host to catch Ctrl/Cmd+n (and browsers keep
      // some of them), so Alt+n works there too.
      if (!host && /^[0-9]$/.test(e.key) && (e.ctrlKey || e.metaKey || e.altKey)) {
        e.preventDefault()
        st.pressShortcut(Number(e.key))
        return
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        st.moveCursor(e.key === 'ArrowDown' ? 1 : -1)
      } else if (e.key === 'Enter') {
        e.preventDefault()
        st.activateCursor()
      }
    }
    // A mouse's side buttons go Back and Forward, like a browser.
    const onMouse = (e: MouseEvent) => {
      if (e.button === 3) useStore.getState().goBack()
      if (e.button === 4) useStore.getState().goForward()
    }
    window.addEventListener('keydown', onKey)
    window.addEventListener('mouseup', onMouse)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('mouseup', onMouse)
    }
  }, [])

  // The connection state, once painted, for the headless smoke run to wait on.
  useEffect(() => {
    const id = requestAnimationFrame(() => requestAnimationFrame(() => (document.documentElement.dataset.conn = conn)))
    return () => cancelAnimationFrame(id)
  }, [conn])

  const page = useStore((s) => (isPage(s.selected) ? s.selected.kind : null))
  const focused = useStore((s) => s.selected.kind !== 'commander' && !isPage(s.selected))
  const narrow = useStore((s) => s.narrow)
  const chatTab = useStore((s) => s.chatTab)
  const nav = clamp(sizes.nav, NAV.min, Math.min(NAV.max, winW - RAIL - MIDDLE_MIN))
  const chat = clamp(sizes.chat, CHAT.min, Math.min(CHAT.max, winW - RAIL - nav - PANE_SPLIT_MIN))
  const save = useCallback(() => {
    setSizes((s) => {
      try {
        localStorage.setItem(sizesKey, JSON.stringify(s))
      } catch {
        // sizes just won't stick
      }
      return s
    })
  }, [])
  const isNarrow = winW - RAIL - nav - CHAT.min < PANE_SPLIT_MIN
  useEffect(() => useStore.getState().setNarrow(isNarrow), [isNarrow])
  const tabbed = focused && narrow
  const dragNav = useCallback((dx: number) => setSizes((s) => ({ ...s, nav: clamp(s.nav + dx, NAV.min, NAV.max) })), [])
  const dragChat = useCallback((dx: number) => setSizes((s) => ({ ...s, chat: clamp(s.chat + dx, CHAT.min, CHAT.max) })), [])

  if (conn === 'no-token') return <NoToken />

  return (
    <div className={`app platform-${platform}${host ? ' in-host' : ' in-web'}`}>
      <TopBar left={RAIL + nav + 1} chat={chat} />
      <Rail />
      <div className="app-body" style={{ gridTemplateColumns: `${nav}px 1px minmax(${MIDDLE_MIN}px, 1fr)` }}>
        <aside className="col col-nav">
          <Navigator />
        </aside>
        <Splitter side="left" onDrag={dragNav} onDone={save} />
        {/* With something in focus, the focus pane and the chat; with nothing in
            focus, the chat alone. Only hidden under a page, so the chat keeps its
            draft and its scroll. */}
        <main className="col col-main">
          {page === 'workflows' && <WorkflowsView />}
          {page === 'progress' && (stateUnavailable ? <><ActivityFreshness /><p role="status">Live state unavailable.</p></> : <ProgressView />)}
          <div
            className={`main-row${tabbed ? (chatTab ? ' tabbed show-chat' : ' tabbed') : ''}${page ? ' under-page' : ''}`}
            style={{ gridTemplateColumns: focused && !narrow ? `minmax(${PANEL_MIN}px, 1fr) 1px ${chat}px` : 'minmax(0, 1fr)' }}
          >
            {focused && (
              <section className="col col-inspector">
                {stateUnavailable ? <><ActivityFreshness /><p role="status">Live state unavailable.</p></> : <InspectorColumn />}
              </section>
            )}
            {focused && !narrow && <Splitter side="right" onDrag={dragChat} onDone={save} />}
            {/* The chat's text column (messages and input) has its own cap, narrower than the
                chat pane can grow, so a wide pane never stretches the text or the input. */}
            <section className="col col-middle" style={{ ['--chat-col' as string]: `${Math.min((tabbed ? winW - RAIL - nav : chat) - 48, CHAT_COL_MAX)}px` }}>
              <ChatControls />
              <div className="chat-slot">
                <ChatSlot />
              </div>
            </section>
          </div>
        </main>
      </div>
      <StartScreen />
    </div>
  )
}

// ChatControls: the chat's own controls, top right of the chat (no title).
function ChatControls() {
  return (
    <div className="chat-controls">
      <PendingChip />
      {/* The chat's controls (Move to app, the ⋯ stats menu) render here. */}
      <div id={HEADER_SLOT_ID} />
    </div>
  )
}

// PendingChip: approvals the selected agent is waiting on (polled GET /api/chat).
function PendingChip() {
  const target = useChatTarget()
  const st = useStore((s) => s.chats[target.role])
  const pending = st?.running ? (st.pending?.length ?? 0) : 0
  if (!pending) return null
  return (
    <span className="chat-chip attn" title={`${target.role} is waiting on you`}>
      <span className="chat-chip-dot" />
      {pending} waiting on you
    </span>
  )
}

// useTheme applies the saved theme (or ?theme=) as <html data-theme>. On macOS
// the Electron window has vibrancy under a transparent page, which only the
// glass theme lets through (data-vibrancy).
function useTheme() {
  useEffect(() => {
    applyTheme(loadTheme())
    if (host && isMac()) document.documentElement.dataset.vibrancy = ''
  }, [])
}

function NoToken() {
  return (
    <div className="no-token">
      <div className="placeholder-card">
        <h2>Open Commander from its link</h2>
        <p>
          This page needs the cmdrd token. Run <code>cmdr app --web</code> on the machine running cmdrd and open the link it prints (over an SSH
          port-forward if needed).
        </p>
      </div>
    </div>
  )
}
