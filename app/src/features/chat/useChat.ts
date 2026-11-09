import { useCallback, useEffect, useRef, useState } from 'react'
import type { ChatEvent, LocalAction } from './events'
import { initialState, reduce, reduceAll, type ChatState } from './events'
import type { ApproveRequest, ChatTransport, ConnState, Attachment } from './transport'
import type { ChatHost } from './host'
import { withScreen } from './screen'

let clientSeq = 0
const newClientId = () => `${Date.now().toString(36)}-${(++clientSeq).toString(36)}`

// useChat subscribes to one role's event stream. Incoming events are batched
// per animation frame and folded with reduceAll, so a burst of text deltas
// costs one render, not one per token.
// How far back a chat opens, in events, and how much more each scroll-up loads.
const OPEN_WITH = 2000
const PAGE = 3000

// Earlier is the history not loaded yet: has is whether there is any.
export interface Earlier {
  has: boolean
  loading: boolean
  version: number // bumps each time a page lands
  load(): void
}

export function useChat(role: string, transport: ChatTransport, opts: { holdWhileDown?: boolean; lastSeq?: number; screen?: (role: string) => string; capture?: ChatHost['capture'] } = {}) {
  const [state, setState] = useState<ChatState>(() => initialState())
  // The chat opens with its recent history only. Everything applied since is
  // kept, so a page of older events can be folded in underneath it.
  const lastSeqRef = useRef(opts.lastSeq)
  lastSeqRef.current = opts.lastSeq
  const screenRef = useRef(opts.screen)
  screenRef.current = opts.screen
  const captureRef = useRef(opts.capture)
  captureRef.current = opts.capture
  const applied = useRef<(ChatEvent | LocalAction)[]>([])
  const older = useRef<ChatEvent[]>([])
  const oldest = useRef(0) // events with seq <= this are not loaded
  const [earlier, setEarlier] = useState({ has: false, loading: false, version: 0 })
  const [conn, setConn] = useState<ConnState>('connecting')
  const [error, setError] = useState<string | undefined>()
  const buf = useRef<(ChatEvent | LocalAction)[]>([])
  const frame = useRef<number | undefined>(undefined)

  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  // Sends made while the stream is down wait here, in order, and go out once it
  // is back (or never reorder behind a later send while the outbox drains).
  const outbox = useRef<(() => Promise<void>)[]>([])
  const draining = useRef(false)
  const connRef = useRef<ConnState>('connecting')
  const hold = !!opts.holdWhileDown

  // A local action (a send, an answer) is applied now and logged like events,
  // so loading earlier history keeps it.
  const local = useCallback((a: LocalAction) => {
    applied.current.push(a)
    setState((s) => reduce(s, a))
  }, [])

  const flush = useCallback(() => {
    if (frame.current !== undefined && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(frame.current)
    if (timer.current !== undefined) clearTimeout(timer.current)
    frame.current = undefined
    timer.current = undefined
    const evs = buf.current
    buf.current = []
    if (!evs.length) return
    applied.current.push(...evs)
    setState((s) => reduceAll(s, evs))
  }, [])

  // Flush on the next animation frame, with a timer fallback: rAF does not
  // fire in hidden/background windows, and events must still land.
  const enqueue = useCallback(
    (evs: (ChatEvent | LocalAction)[]) => {
      buf.current.push(...evs)
      if (timer.current !== undefined) return
      timer.current = setTimeout(flush, 50)
      if (typeof requestAnimationFrame === 'function') frame.current = requestAnimationFrame(flush)
    },
    [flush],
  )

  const drain = useCallback(async () => {
    if (draining.current) return
    draining.current = true
    try {
      while (outbox.current.length && connRef.current === 'open') await outbox.current.shift()!()
    } finally {
      draining.current = false
    }
  }, [])

  useEffect(() => {
    setState(initialState())
    buf.current = []
    outbox.current = []
    applied.current = []
    older.current = []
    oldest.current = transport.history && lastSeqRef.current ? Math.max(0, lastSeqRef.current - OPEN_WITH) : 0
    setEarlier({ has: oldest.current > 0, loading: false, version: 0 })
    const unsub = transport.subscribe(role, oldest.current, enqueue, (c) => {
      connRef.current = c
      setConn(c)
      if (c === 'open') void drain()
    })
    return () => {
      unsub()
      if (frame.current !== undefined && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(frame.current)
      if (timer.current !== undefined) clearTimeout(timer.current)
      frame.current = undefined
      timer.current = undefined
    }
  }, [role, transport, enqueue, drain])

  const guard = useCallback(
    async (p: Promise<unknown>) => {
      try {
        setError(undefined)
        await p
      } catch (e) {
        setError(friendlyError(e instanceof Error ? e.message : String(e), role))
      }
    },
    [role],
  )

  const send = useCallback(
    (text: string, opts: { attachments?: Attachment[]; now?: boolean; noCapture?: boolean } = {}) => {
      const clientId = newClientId()
      local({ kind: 'local_send', clientId, text, attachments: opts.attachments, ts: Date.now() })
      // What to capture is decided now, at the press: a held message drained later must not capture another tab.
      const cap0 = opts.noCapture ? null : captureRef.current?.snapshot ? captureRef.current.snapshot(role) : undefined
      // Read when it goes out, so a held message carries the screen as it is then.
      const go = async () => {
        // The page capture waits at most its own timeout; without it the message goes out as it is.
        const cap = opts.noCapture || cap0 === null ? null : await captureRef.current?.take(role, cap0).catch(() => null)
        const screen = [screenRef.current?.(role) ?? '', cap?.note ?? ''].filter(Boolean).join('\n')
        const files = cap ? [...(opts.attachments ?? []), ...cap.attachments] : opts.attachments
        return guard(
          transport.send(role, { text: withScreen(screen, text), clientId, attachments: files, now: opts.now }).then((r) => {
            // A terminal chat has no stream to echo the message: mark it sent by mail.
            if (r && r.delivered === 'mail') local({ kind: 'local_delivered', clientId, via: 'mail', to: role })
          }),
        )
      }
      if (hold && (connRef.current !== 'open' || outbox.current.length || draining.current)) {
        outbox.current.push(go)
        return Promise.resolve()
      }
      return go()
    },
    [role, transport, guard, hold, local],
  )

  const interrupt = useCallback(() => guard(transport.interrupt(role)), [role, transport, guard])

  const loadEarlier = useCallback(() => {
    if (!transport.history || oldest.current <= 0) return
    const until = oldest.current
    const since = Math.max(0, until - PAGE)
    setEarlier((e) => (e.loading ? e : { ...e, loading: true }))
    transport.history(role, since, until).then(
      (evs) => {
        if (oldest.current !== until) return // a newer subscription replaced this one
        older.current = [...evs, ...older.current]
        oldest.current = since
        flush()
        setState(reduceAll(initialState(), [...older.current, ...applied.current]))
        setEarlier((e) => ({ has: since > 0, loading: false, version: e.version + 1 }))
      },
      (err) => {
        setEarlier((e) => ({ ...e, loading: false }))
        setError(err instanceof Error ? err.message : String(err))
      },
    )
  }, [role, transport, flush])

  const approve = useCallback(
    (req: ApproveRequest & { always?: boolean }) => {
      const { always, ...wire } = req
      local({ kind: 'local_resolve', id: req.id, behavior: req.behavior, always, message: req.message })
      return guard(transport.approve(role, wire))
    },
    [role, transport, guard, local],
  )

  const answer = useCallback(
    (id: string, answers: Record<string, string>) => {
      local({ kind: 'local_resolve', id, behavior: 'allow', message: Object.values(answers).join('; ') })
      return guard(transport.answer(role, { id, answers }))
    },
    [role, transport, guard, local],
  )

  return { state, conn, error, send, interrupt, approve, answer, earlier: { ...earlier, load: loadEarlier } as Earlier }
}

// friendlyError: never show cmdrd's raw "no running process" to the user.
export function friendlyError(msg: string, role: string): string {
  if (/no running process|not running/i.test(msg)) return `Not sent: ${role} has no running chat to deliver to.`
  return msg
}
