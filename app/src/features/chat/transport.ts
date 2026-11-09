// ChatTransport: how a ChatPane talks to one agent's chat session.
// WsChatTransport speaks cmdrd's /api/chat/{role}/... API; ReplayTransport
// plays a recorded event list (tests, fixtures, dev without cmdrd).
import { EngineMapper } from './engine'
import type { ChatEvent, EventInput, PermissionSuggestion } from './events'

// A file picked, dropped or pasted into the composer.
export interface Attachment {
  name: string
  mediaType: string
  dataUrl: string
}

export interface SendRequest {
  text: string
  clientId: string
  attachments?: Attachment[]
  now?: boolean // interrupt the running turn and send immediately
}

export interface ApproveRequest {
  id: string // request id from permission_request / plan
  behavior: 'allow' | 'deny'
  updatedInput?: unknown
  updatedPermissions?: PermissionSuggestion[] // "Always allow": the chosen suggestions
  message?: string // deny reason / "keep planning" feedback
}

export interface AnswerRequest {
  id: string // request id from a question event
  answers: Record<string, string> // question text -> answer (multi-select joined by ", ")
}

export type ConnState = 'connecting' | 'open' | 'closed' | 'error'

// SendResult: how a sent message reached the agent. delivered "mail": the
// role has no native process yet (a terminal chat), so cmdrd mailed it as the user.
export interface SendResult {
  delivered?: 'mail'
}

export interface ChatTransport {
  subscribe(role: string, since: number, onEvent: (events: ChatEvent[]) => void, onConn?: (s: ConnState) => void): () => void
  send(role: string, req: SendRequest): Promise<SendResult | void>
  interrupt(role: string): Promise<void>
  approve(role: string, req: ApproveRequest): Promise<void>
  answer(role: string, req: AnswerRequest): Promise<void>
  // Events with since < seq <= until: older history, fetched when the user scrolls up.
  history?(role: string, since: number, until: number): Promise<ChatEvent[]>
}

// ---- WebSocket / HTTP --------------------------------------------------------

export interface WsOptions {
  baseUrl: string // e.g. http://127.0.0.1:7777 ('' = same origin, the /app/ web mode)
  token: string
  WebSocketImpl?: typeof WebSocket
  fetchImpl?: typeof fetch
}

export class WsChatTransport implements ChatTransport {
  constructor(private opts: WsOptions) {}

  private url(path: string): string {
    const base = this.opts.baseUrl || (typeof location !== 'undefined' ? location.origin : '')
    return base.replace(/\/$/, '') + path
  }

  subscribe(role: string, since: number, onEvent: (events: ChatEvent[]) => void, onConn?: (s: ConnState) => void): () => void {
    const WS = this.opts.WebSocketImpl ?? WebSocket
    let closed = false
    let ws: WebSocket | undefined
    let lastSeq = since
    let retry = 0
    let timer: ReturnType<typeof setTimeout> | undefined
    const mapper = new EngineMapper()

    const connect = () => {
      onConn?.('connecting')
      // Browsers cannot set headers on a WebSocket, so the token rides in the
      // query string (cmdrd's auth accepts ?token=).
      const u = new URL(this.url(`/api/chat/${encodeURIComponent(role)}/events`))
      u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
      u.searchParams.set('since', String(lastSeq))
      u.searchParams.set('token', this.opts.token)
      ws = new WS(u.toString())
      // History arrives as many frames, then cmdrd's replay_done marker. It is
      // handed on in one piece, so the pane never shows half a history (an old
      // turn that looks unfinished, with a timer counting from hours ago).
      let history: ChatEvent[] | null = []
      let quiet: ReturnType<typeof setTimeout> | undefined
      const flushHistory = () => {
        clearTimeout(quiet)
        if (history?.length) onEvent(history)
        history = null
      }
      ws.onopen = () => {
        retry = 0
        onConn?.('open')
      }
      ws.onmessage = (m) => {
        let frame: unknown
        try {
          frame = JSON.parse(typeof m.data === 'string' ? m.data : '')
        } catch {
          return
        }
        if ((frame as { kind?: string })?.kind === 'replay_done') return flushHistory()
        const evs = mapper.map(frame, role)
        if (!evs.length) return
        lastSeq = Math.max(lastSeq, ...evs.map((e) => e.seq))
        if (!history) return onEvent(evs)
        history.push(...evs)
        // No marker (an older cmdrd): history is done once frames stop coming.
        clearTimeout(quiet)
        quiet = setTimeout(flushHistory, 150)
      }
      ws.onerror = () => onConn?.('error')
      ws.onclose = () => {
        flushHistory()
        if (closed) return
        onConn?.('closed')
        const delay = Math.min(10_000, 250 * 2 ** retry++)
        timer = setTimeout(connect, delay)
      }
    }
    connect()
    return () => {
      closed = true
      if (timer) clearTimeout(timer)
      ws?.close()
    }
  }

  // cmdrd has no ranged read: this opens the stream at since and keeps the
  // events up to until, closing as soon as it has them.
  history(role: string, since: number, until: number): Promise<ChatEvent[]> {
    const WS = this.opts.WebSocketImpl ?? WebSocket
    const u = new URL(this.url(`/api/chat/${encodeURIComponent(role)}/events`))
    u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:'
    u.searchParams.set('since', String(since))
    u.searchParams.set('token', this.opts.token)
    return new Promise((resolve, reject) => {
      const mapper = new EngineMapper()
      const out: ChatEvent[] = []
      const ws = new WS(u.toString())
      const done = () => {
        clearTimeout(timeout)
        ws.onmessage = null
        ws.onclose = null
        ws.close()
        resolve(out)
      }
      const timeout = setTimeout(() => {
        ws.close()
        reject(new Error('loading earlier messages timed out'))
      }, 20_000)
      ws.onmessage = (m) => {
        let frame: unknown
        try {
          frame = JSON.parse(typeof m.data === 'string' ? m.data : '')
        } catch {
          return
        }
        const f = frame as { kind?: string; seq?: number }
        if (f?.kind === 'replay_done' || (typeof f?.seq === 'number' && f.seq > until)) return done()
        out.push(...mapper.map(frame, role).filter((e) => e.seq <= until))
      }
      ws.onclose = done
      ws.onerror = () => {
        clearTimeout(timeout)
        reject(new Error('could not load earlier messages'))
      }
    })
  }

  private async post(role: string, verb: string, body: unknown): Promise<Record<string, unknown>> {
    const f = this.opts.fetchImpl ?? fetch
    const res = await f(this.url(`/api/chat/${encodeURIComponent(role)}/${verb}`), {
      method: 'POST',
      headers: { Authorization: `Bearer ${this.opts.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!res.ok) {
      let msg = `${verb} failed: HTTP ${res.status}`
      try {
        const j = await res.json()
        if (j?.error) msg = String(j.error)
      } catch {
        /* no body */
      }
      throw new Error(msg)
    }
    return ((await res.json().catch(() => null)) ?? {}) as Record<string, unknown>
  }

  // cmdrd's bodies (internal/api/chat.go chatRequest + chat.Decision).
  async send(role: string, req: SendRequest): Promise<SendResult> {
    const j = await this.post(role, 'send', {
      text: req.text,
      client_id: req.clientId,
      attachments: req.attachments?.map((a) => ({ name: a.name, mime: a.mediaType, data: a.dataUrl.slice(a.dataUrl.indexOf(',') + 1) })),
    })
    return j.delivered === 'mail' ? { delivered: 'mail' } : {}
  }
  async interrupt(role: string) {
    await this.post(role, 'interrupt', {})
  }
  async approve(role: string, req: ApproveRequest) {
    await this.post(role, 'approve', {
      id: req.id,
      decision: req.behavior === 'allow' && req.updatedPermissions?.length ? 'allow_session' : req.behavior,
      message: req.message,
      updated_input: req.updatedInput,
      updated_permissions: req.updatedPermissions,
    })
  }
  async answer(role: string, req: AnswerRequest) {
    await this.post(role, 'answer', req)
  }
}

// ---- Replay --------------------------------------------------------------------

export interface ReplayOptions {
  // 0 = deliver everything at once (default); otherwise a playback speed
  // multiplier over the events' ts gaps (1 = real time).
  speed?: number
  // Echo sends back as user_message events (like cmdrd does).
  echo?: boolean
}

export type ReplayCall =
  | { verb: 'send'; role: string; req: SendRequest }
  | { verb: 'interrupt'; role: string }
  | { verb: 'approve'; role: string; req: ApproveRequest }
  | { verb: 'answer'; role: string; req: AnswerRequest }

export class ReplayTransport implements ChatTransport {
  calls: ReplayCall[] = []
  private listeners = new Set<(e: ChatEvent[]) => void>()
  private seq: number

  constructor(private events: ChatEvent[], private opts: ReplayOptions = {}) {
    this.seq = events.reduce((m, e) => Math.max(m, e.seq), 0)
  }

  subscribe(_role: string, since: number, onEvent: (events: ChatEvent[]) => void, onConn?: (s: ConnState) => void): () => void {
    this.listeners.add(onEvent)
    onConn?.('open')
    const evs = this.events.filter((e) => e.seq > since)
    const timers: ReturnType<typeof setTimeout>[] = []
    if (!this.opts.speed) {
      if (evs.length) queueMicrotask(() => this.listeners.has(onEvent) && onEvent(evs))
    } else {
      const t0 = evs[0]?.ts ?? 0
      for (const e of evs) timers.push(setTimeout(() => onEvent([e]), ((e.ts ?? t0) - t0) / this.opts.speed))
    }
    return () => {
      this.listeners.delete(onEvent)
      timers.forEach(clearTimeout)
    }
  }

  // emit pushes extra events to live subscribers (tests drive the UI with it).
  emit(events: EventInput[] | EventInput) {
    const list = (Array.isArray(events) ? events : [events]).map((e) => ({ session: 'replay', ...e, seq: ++this.seq }) as ChatEvent)
    this.events.push(...list)
    for (const l of this.listeners) l(list)
  }

  async send(role: string, req: SendRequest): Promise<SendResult | void> {
    this.calls.push({ verb: 'send', role, req })
    if (this.opts.echo) this.emit({ kind: 'user_message', id: `echo-${req.clientId}`, clientId: req.clientId, text: req.text, attachments: req.attachments?.map(({ name, mediaType }) => ({ name, mediaType })), ts: Date.now() })
  }
  async interrupt(role: string) {
    this.calls.push({ verb: 'interrupt', role })
  }
  async approve(role: string, req: ApproveRequest) {
    this.calls.push({ verb: 'approve', role, req })
  }
  async answer(role: string, req: AnswerRequest) {
    this.calls.push({ verb: 'answer', role, req })
  }
}
