// The one renderer store: the latest snapshot, the connection state and the
// navigator state. Everything on screen derives from it.

import { create } from 'zustand'
import type { FocusTarget } from './components/FocusHeader'
import type { HostConfig, Snapshot } from '../../shared/snapshot'
import { loadConfig } from './host'
import type { ChatAccount, ChatStatus, Provider } from './chatApi'
import { activateWorkstream, board, deriveSnapshot, askKey, linkTarget, navOrder, selectionKey, shortcut, withLocalClocks, wsKey, type LinkTab, type NavState, type Selection } from './viewmodel'

export type Conn = 'connecting' | 'live' | 'offline' | 'no-token'

export interface GraphFocus {
  ws: string // workstream key
  bead?: string // the bead to centre on (absent: the whole workstream)
  sel: string // selectionKey when the link was clicked
  at: number // click time, so clicking the same link again re-centres
}

interface Store extends NavState {
  snapshot: Snapshot | null
  config: HostConfig | null // cmdrd URL + token, once known
  daemon: string // Electron's cmdrd health ('checking' | 'starting' | 'online' | 'offline'); '' in a browser
  conn: Conn
  retryAt: number // ms epoch of the next reconnect attempt while offline
  nowSec: number
  cursor: string // keyboard cursor row (a navOrder key)
  fresh: Record<string, true> // asks that appeared after the first snapshot: their ⚑ pulses once
  completed: Record<string, number> // workstreams and tracks the user closed with Complete (project:bead → when): hidden at once, counted by the name
  leaving: Record<string, true> // just completed: the row plays its exit before it goes
  markCompleted(key: string): void
  completeError: Record<string, string> // a Complete whose close failed: the row is back, with why
  failCompleted(key: string, why: string): void
  chatEngine: 'checking' | 'on' | 'off' // cmdrd's native chat engine (GET /api/chat)
  chats: Record<string, ChatStatus> // by role, polled
  accounts: ChatAccount[] // Claude accounts and Codex, with usage (polled with chats)
  providers: Record<string, Provider> // what each role runs on (polled with chats)
  // A link in a chat moved the Graph tab: to this workstream, centred on this
  // bead. It holds only while the selection it was made under (sel) stays.
  graphFocus: GraphFocus | null
  focusGraph(beadId: string): void
  setSnapshot(s: Snapshot): void
  setConn(c: Conn, retryAt?: number): void
  reconnectNow?: () => void // set by connect(): skip the backoff wait
  tick(): void
  clickWorkstream(key: string): void
  // The sidebar header last clicked: the panel opens its Workflow tab (track = a track's bead).
  workflowOpen: { ws: string; track?: string; tab?: 'overview' | 'workflow'; at: number } | null
  // A web page a link asked for, opened as a tab once its workstream is on screen.
  pendingPage: { ws: string; url: string; title?: string } | null
  // A link to a bead: its workstream or track, on the tab named (or its decision when
  // one waits on that bead, else Overview). The same place every time.
  openLink(beadId: string, tab?: LinkTab): void
  // A link to a page, in the context of a bead: that workstream, with the page open.
  openLinkPage(beadId: string, url: string, title?: string): boolean
  clearPendingPage(): void
  // What the app is focused on (named in the top bar); null = no focus.
  focus: FocusTarget | null
  setFocus(f: FocusTarget | null): void
  // Home: out of focus, back to Commander's chat.
  goHome(): void
  // A window too narrow for the pane beside the chat: the chat is the pane's first tab.
  narrow: boolean
  setNarrow(narrow: boolean): void
  chatTab: boolean // in a narrow window, the Chat tab is the one showing
  setChatTab(on: boolean): void
  openWorkflow(key: string, track?: string, tab?: 'overview' | 'workflow'): void
  closeWorkflow(): void
  select(sel: Selection): void
  selectedAt: number // every select, even of the same thing: the panel brings its tab forward again
  pressShortcut(n: number): void
  stepWorkstream(delta: number): void
  goWorkstream(key: string): void
  moveCursor(delta: number): void
  activateCursor(): void
  back: Selection[] // where Back goes, most recent last
  forward: Selection[] // where Forward goes, next first
  goBack(): void
  goForward(): void
}

// The store must be one object for the app's whole life: a hot update would
// create a second, empty store that later-updated components read from (the
// Graph tab went blank that way). Any change here reloads the page instead.
import.meta.hot?.accept(() => location.reload())

export const useStore = create<Store>((set, get) => ({
  snapshot: null,
  completed: loadCompleted(),
  leaving: {},
  completeError: {},
  failCompleted(key, why) {
    const { [key]: _done, ...completed } = get().completed
    const { [key]: _going, ...leaving } = get().leaving
    saveCompleted(completed)
    set({ completed, leaving, completeError: { ...get().completeError, [key]: why } })
  },
  markCompleted(key) {
    const { [key]: _old, ...completeError } = get().completeError
    set({ completeError })
    set({ leaving: { ...get().leaving, [key]: true } })
    // The row fades out with the confetti, then leaves the sidebar.
    setTimeout(() => {
      if (!get().leaving[key]) return // its close already failed and the row came back
      const completed = { ...get().completed, [key]: Date.now() }
      saveCompleted(completed)
      const { [key]: _gone, ...leaving } = get().leaving
      set({ completed, leaving })
    }, 900)
  },
  config: null,
  daemon: '',
  conn: 'connecting',
  retryAt: 0,
  nowSec: Math.floor(Date.now() / 1000),
  selected: { kind: 'commander' },
  cursor: 'commander',
  fresh: {},
  chatEngine: 'checking',
  chats: {},
  accounts: [],
  providers: {},
  back: [],
  forward: [],
  graphFocus: null,
  workflowOpen: null,
  pendingPage: null,
  focus: null,
  narrow: false,
  chatTab: true,
  selectedAt: 0,

  focusGraph(beadId) {
    const st = get()
    const ws = st.snapshot?.workstreams.find((w) => beadId === w.bead_id || beadId.startsWith(`${w.bead_id}.`))
    if (!ws) return
    set({ graphFocus: { ws: wsKey(ws), bead: beadId === ws.bead_id ? undefined : beadId, sel: selectionKey(st.selected), at: Date.now() } })
  },

  setSnapshot(raw) {
    const st = get()
    const s = withLocalClocks(deriveSnapshot(raw))
    const fresh: Record<string, true> = {}
    if (st.snapshot) {
      const before = new Set(st.snapshot.needs_you.map(askKey))
      for (const a of s.needs_you ?? []) if (!before.has(askKey(a))) fresh[askKey(a)] = true
    }
    set({ snapshot: s, fresh: { ...st.fresh, ...fresh }, nowSec: Math.floor(Date.now() / 1000) })
  },
  setConn(conn, retryAt = 0) {
    set({ conn, retryAt })
  },
  tick() {
    set({ nowSec: Math.floor(Date.now() / 1000) })
  },
  clickWorkstream(key) {
    const st = get()
    set({ ...activateWorkstream(st, key), cursor: key })
  },
  openWorkflow(key, track, tab) {
    get().clickWorkstream(key)
    set({ workflowOpen: { ws: key, track, tab, at: Date.now() }, chatTab: false })
  },
  openLink(beadId, tab) {
    const st = get()
    const t = linkTarget(st.snapshot, beadId)
    if (!t) return
    if (t.ask && (!tab || tab === 'decision')) {
      st.select({ kind: 'ask', key: askKey(t.ask) })
      return
    }
    if (t.ws) st.openWorkflow(wsKey(t.ws), t.track, tab === 'workflow' ? 'workflow' : 'overview')
  },
  openLinkPage(beadId, url, title) {
    const st = get()
    const t = linkTarget(st.snapshot, beadId)
    if (!t?.ws) return false
    st.openWorkflow(wsKey(t.ws), t.track)
    set({ pendingPage: { ws: wsKey(t.ws), url, title } })
    return true
  },
  clearPendingPage() {
    set({ pendingPage: null })
  },
  closeWorkflow() {
    set({ workflowOpen: null })
  },
  setFocus(focus) {
    set({ focus })
  },
  goHome() {
    get().select({ kind: 'commander' })
    set({ workflowOpen: null })
  },
  setNarrow(narrow) {
    if (narrow !== get().narrow) set({ narrow, chatTab: true })
  },
  setChatTab(chatTab) {
    set({ chatTab })
  },
  select(sel) {
    set({ selected: sel, cursor: selectionKey(sel), selectedAt: Date.now() })
  },
  pressShortcut(n) {
    const st = get()
    if (!st.snapshot) return
    const next = shortcut(st, board(st.snapshot), n)
    set({ ...next, cursor: n === 0 ? 'commander' : (next.selected.kind === 'ws' ? next.selected.key : st.cursor) })
  },
  stepWorkstream(delta) {
    const st = get()
    if (!st.snapshot) return
    const keys = board(st.snapshot).workstreams.map((w) => w.key)
    if (keys.length === 0) return
    const cur = st.selected.kind === 'ws' ? keys.indexOf(st.selected.key) : -1
    const next = keys[(cur + delta + keys.length) % keys.length] ?? keys[0]
    st.goWorkstream(next)
  },
  goWorkstream(key) {
    set({ selected: { kind: 'ws', key }, cursor: key })
  },
  moveCursor(delta) {
    const st = get()
    if (!st.snapshot) return
    const order = navOrder(board(st.snapshot))
    const i = Math.max(order.indexOf(st.cursor), 0)
    const next = order[Math.min(Math.max(i + delta, 0), order.length - 1)]
    set({ cursor: next })
  },
  goBack() {
    const st = get()
    const to = st.back[st.back.length - 1]
    if (!to) return
    travel(() => set({ ...land(to), back: st.back.slice(0, -1), forward: [st.selected, ...st.forward] }))
  },
  goForward() {
    const st = get()
    const to = st.forward[0]
    if (!to) return
    travel(() => set({ ...land(to), back: [...st.back, st.selected], forward: st.forward.slice(1) }))
  },
  activateCursor() {
    const { cursor } = get()
    if (cursor === 'commander') get().select({ kind: 'commander' })
    else if (cursor.startsWith('ask:')) get().select({ kind: 'ask', key: cursor })
    else get().clickWorkstream(cursor)
  },
}))

// What the focus pane is on survives a reload: the selection and an open workflow.
const FOCUS_KEY = 'commander.focus.v1'

function loadFocus(): Partial<Store> {
  try {
    const v = JSON.parse(localStorage.getItem(FOCUS_KEY) ?? 'null')
    if (!v?.selected?.kind) return {}
    return { selected: v.selected, cursor: selectionKey(v.selected), workflowOpen: v.workflowOpen ? { ...v.workflowOpen, at: Date.now() } : null }
  } catch {
    return {}
  }
}

useStore.setState(loadFocus())
useStore.subscribe((st, prev) => {
  if (st.selected === prev.selected && st.workflowOpen === prev.workflowOpen) return
  try {
    localStorage.setItem(FOCUS_KEY, JSON.stringify({ selected: st.selected, workflowOpen: st.workflowOpen }))
  } catch {
    // a private window: a reload starts on Commander
  }
})

// Back/Forward history: every change of selection, however it happened (a
// click, a shortcut, the canvas), is recorded, except the moves Back and
// Forward make themselves.
const HISTORY = 50
let traveling = false

function travel(fn: () => void) {
  traveling = true
  try {
    fn()
  } finally {
    traveling = false
  }
}

// Landing on a selection puts the cursor on its workstream.
function land(sel: Selection): Partial<Store> {
  return { selected: sel, cursor: selectionKey(sel) }
}

const sameSelection = (a: Selection, b: Selection) => JSON.stringify(a) === JSON.stringify(b)

useStore.subscribe((s, prev) => {
  if (traveling || s.selected === prev.selected || sameSelection(s.selected, prev.selected)) return
  useStore.setState({ back: [...prev.back, prev.selected].slice(-HISTORY), forward: [] })
})

// A refused loopback connect costs nothing, and a restarted cmdrd answers
// within about a second: a longer wait only keeps the start screen up.
const MAX_RETRY_MS = 2_000

/**
 * connect keeps a WebSocket to cmdrd's /api/events open, reconnecting with
 * backoff (0.5s doubling to 2s). The config is re-read on every attempt, so a
 * cmdrd that restarted on another port or with a new token is picked up.
 */
export function connect(onFirstRender?: (ok: boolean, detail: string) => void): () => void {
  const store = useStore.getState()
  let ws: WebSocket | null = null
  let timer: ReturnType<typeof setTimeout> | undefined
  let delay = 500
  let stopped = false
  let reported = false
  const report = (ok: boolean, detail: string) => {
    if (reported || !onFirstRender) return
    reported = true
    onFirstRender(ok, detail)
  }

  const retry = () => {
    if (stopped) return
    store.setConn('offline', Date.now() + delay)
    report(false, 'cmdrd offline')
    timer = setTimeout(open, delay)
    delay = Math.min(delay * 2, MAX_RETRY_MS)
  }

  const open = async () => {
    if (stopped) return
    let cfg: HostConfig | null
    try {
      cfg = await loadConfig()
    } catch {
      cfg = null
    }
    if (cfg?.fixture && !useStore.getState().snapshot) store.setSnapshot(cfg.fixture)
    const prev = useStore.getState().config
    if (cfg && cfg.token && (prev?.apiUrl !== cfg.apiUrl || prev?.token !== cfg.token)) useStore.setState({ config: cfg })
    if (cfg?.demo) {
      store.setConn('live')
      report(true, 'fixture')
      return
    }
    if (!cfg || !cfg.token) {
      store.setConn('no-token')
      report(false, 'no token')
      return
    }
    const url = new URL('/api/events', cfg.apiUrl)
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
    url.searchParams.set('token', cfg.token)
    if (useStore.getState().conn !== 'offline') store.setConn('connecting')
    const sock = new WebSocket(url)
    ws = sock
    sock.onopen = () => {
      delay = 500
      // cmdrd builds its first snapshot after a restart cold (br and the
      // store under load: a minute or more), and sends it only then. It is up
      // now, so keep showing the snapshot we hold instead of the start screen.
      if (useStore.getState().snapshot) useStore.getState().setConn('live')
    }
    sock.onmessage = (ev) => {
      try {
        const msg = JSON.parse(String(ev.data)) as { type: string; snapshot?: Snapshot }
        if (msg.type === 'snapshot' && msg.snapshot) {
          delay = 500
          useStore.getState().setSnapshot(msg.snapshot)
          useStore.getState().setConn('live')
          report(true, `live v${msg.snapshot.version}`)
        }
      } catch {
        // a malformed frame is dropped; the next snapshot replaces it
      }
    }
    sock.onclose = () => {
      if (ws === sock) {
        ws = null
        retry()
      }
    }
  }

  useStore.setState({
    reconnectNow: () => {
      if (stopped || ws) return
      clearTimeout(timer)
      delay = 500
      void open()
    },
  })
  void open()
  const clock = setInterval(() => useStore.getState().tick(), 15_000)
  return () => {
    stopped = true
    clearTimeout(timer)
    clearInterval(clock)
    ws?.close()
  }
}

/** useRepo is a project's GitHub owner/name, for PR links ("" until known). */
export function useRepo(project?: string): string {
  return useStore((s) => (project ? (s.snapshot?.projects.find((p) => p.name === project)?.repo ?? '') : ''))
}

// The user's completed workstreams and tracks live in this app's storage: the
// count by the name is theirs, and survives reloads.
const COMPLETED_KEY = 'cmdr.completed'

function loadCompleted(): Record<string, number> {
  try {
    const v = JSON.parse(localStorage.getItem(COMPLETED_KEY) ?? 'null')
    if (v && typeof v === 'object') return v as Record<string, number>
  } catch {
    // unreadable storage: start empty
  }
  return {}
}

function saveCompleted(v: Record<string, number>): void {
  try {
    localStorage.setItem(COMPLETED_KEY, JSON.stringify(v))
  } catch {
    // a private window: the count starts over on reload
  }
}
