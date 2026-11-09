import { AssistantRuntimeProvider, useExternalStoreRuntime, type ThreadMessageLike } from '@assistant-ui/react'
import { useContext, useEffect, useMemo, useRef, useState } from 'react'
import './chat.css'
import './thread/thread.css'
import { useChat } from './useChat'
import { Header, type MenuItem } from './Header'
import { Thread } from './thread/Thread'
import { ApiRetryRow } from './thread/ApiRetryRow'
import { toThread } from './thread/toThread'
import { Composer } from './Composer'
import { ActionsContext, ItemsContext, type ChatActions } from './context'
import type { ChatTransport } from './transport'
import { FALLBACK_COMMANDS } from './adapter'
import { Icon } from './icons'
import { ChatHostContext } from './host'

export interface ChatPaneProps {
  role: string // cmdrd chat role, e.g. "commander", "manager:web", "op:bd-12"
  transport: ChatTransport
  title?: string
  readonly?: string // e.g. "Owned by a terminal": shows a banner instead of the composer
  menu?: MenuItem[]
  initialRect?: { width: number; height: number } // for tests / SSR measurements
  costNote?: string // e.g. "est. $1.20 today, $14 total": the header tooltip
  costUsd?: number // the host's known session cost (cmdrd status): a floor for the header's figure
  lastSeq?: number // the session's latest event (cmdrd status): the chat opens near it, older history loads on scroll
  // The session lives in a terminal (not this engine): the header says so and
  // sends go by mail (cmdrd answers delivered:"mail"). onMove: the confirmed handover.
  terminal?: TerminalSession
}

export interface TerminalSession {
  label: string // e.g. "terminal session"
  title?: string // tooltip, e.g. "running in its tmux terminal (pid 4242)"
  onMove?: () => void
}

const DOWN_BANNER_MS = 15_000

// useOffline: a brief cmdrd disconnect is normal (the transport reconnects with
// backoff), so it only shows a quiet chip; `down` flips after DOWN_BANNER_MS.
function useOffline(conn: string, terminal: unknown) {
  const [down, setDown] = useState(false)
  const [wasOpen, setWasOpen] = useState(false)
  const reconnecting = !terminal && conn !== 'open'
  useEffect(() => {
    if (conn === 'open') setWasOpen(true)
  }, [conn])
  useEffect(() => {
    if (!reconnecting) {
      setDown(false)
      return
    }
    const t = setTimeout(() => setDown(true), DOWN_BANNER_MS)
    return () => clearTimeout(t)
  }, [reconnecting])
  return { reconnecting, down, wasOpen }
}

export function ChatPane({ role, transport, title, readonly, menu, costUsd, costNote, lastSeq, terminal }: ChatPaneProps) {
  const host = useContext(ChatHostContext)
  const { state, conn, error, send, interrupt, approve, answer, earlier } = useChat(role, transport, { holdWhileDown: !terminal, lastSeq, screen: host.screen, capture: host.capture })
  const actions = useMemo<ChatActions>(() => ({ approve, answer, readonly: !!readonly, now: () => Date.now() }), [approve, answer, readonly])
  const running = !terminal && (state.turnOpen || state.phase !== 'idle') // a terminal chat's stale stream says nothing about now
  // Queued only means something while a reply is in progress (a stale stream can leave entries).
  const waiting = !running
    ? []
    : state.order.flatMap((id) => {
        const it = state.items[id]
        return it?.type === 'user' && it.queued ? [it.text] : []
      })

  // A message cmdrd has not taken yet: say so, so a slow send never reads as an idle chat.
  const sending = state.order.some((id) => {
    const it = state.items[id]
    return it?.type === 'user' && it.pending
  })

  const offline = useOffline(conn, terminal)

  const messages = useMemo(() => toThread(state, running), [state, running])
  // assistant-ui renders the thread from Commander's own state; sending, stop
  // and queueing stay with Commander's composer (slash commands, images, Mod+Enter).
  const runtime = useExternalStoreRuntime<ThreadMessageLike>({
    messages,
    convertMessage: (m) => m,
    isRunning: running,
    onNew: async () => {},
    onCancel: async () => void interrupt(),
  })

  const { stageRef, floatsRef } = useFloatsHeight()

  return (
    <ItemsContext.Provider value={state.items}>
      <ActionsContext.Provider value={actions}>
        <section className={`cmdr-chat${running && state.apiRetry ? ' cc-retrying' : ''}`} aria-label={`Chat: ${title ?? role}`}>
          <Header title={title ?? role} state={costUsd && costUsd > state.costUsd ? { ...state, costUsd } : state} extraMenu={menu} terminal={terminal} costNote={costNote} />
          <div className="cc-stage" ref={stageRef}>
            <AssistantRuntimeProvider runtime={runtime}>
              <Thread
                earlier={earlier}
                tail={
                  sending ? (
                    <div className="ct-activity" role="status">
                      <div className="ct-activity-head">
                        <Icon name="send" size={14} />
                        <span>Sending…</span>
                      </div>
                    </div>
                  ) : running && state.apiRetry ? (
                    <ApiRetryRow retry={state.apiRetry} />
                  ) : !running && host.Watching ? (
                    <host.Watching role={role} />
                  ) : undefined
                }
                empty={
                  <div className="cc-empty">
                    <Icon name="bot" size={22} />
                    <div>{conn === 'open' ? 'No messages yet.' : 'Connecting…'}</div>
                  </div>
                }
              />
            </AssistantRuntimeProvider>
            <div className="cc-floats" ref={floatsRef}>
              {offline.reconnecting && !offline.down && (
                <div className="cc-reconnecting" role="status">
                  {conn === 'connecting' && !offline.wasOpen ? 'connecting…' : 'reconnecting…'}
                </div>
              )}
              {(error || offline.down) && (
                <div className="cc-error-bar" role="alert">
                  <Icon name="alert" size={13} /> {offline.down ? 'Lost connection to cmdrd. Retrying; messages you send are kept and go out in order.' : error}
                </div>
              )}
              {host.Dock && <host.Dock role={role} />}
            </div>
          </div>
          <Composer
            role={role}
            running={running}
            waiting={waiting}
            commands={state.commands.length ? state.commands : FALLBACK_COMMANDS}
            readonly={readonly}
            onSend={(text, opts) => void send(text, opts)}
            onStop={() => void interrupt()}
            placeholder={terminal ? `Message ${title ?? role}… (delivered by mail to its terminal)` : undefined}
            aside={host.MailChip && <host.MailChip role={role} />}
            capture={host.capture?.usePreview(role) ?? null}
            onShare={host.capture?.setEnabled}
            onTyping={host.typing && (() => host.typing!(role))}
          />
        </section>
      </ActionsContext.Provider>
    </ItemsContext.Provider>
  )
}

// The thread runs down to the input bar with the floats over it. Their height
// is published so the newest message can still scroll clear of them.
function useFloatsHeight() {
  const stageRef = useRef<HTMLDivElement>(null)
  const floatsRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const stage = stageRef.current
    const floats = floatsRef.current
    if (!stage || !floats || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => stage.style.setProperty('--cc-floats-h', `${floats.offsetHeight}px`))
    ro.observe(floats)
    return () => ro.disconnect()
  }, [])
  return { stageRef, floatsRef }
}
