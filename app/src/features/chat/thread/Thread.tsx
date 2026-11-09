// The conversation, on assistant-ui primitives: each message you send and
// the reply it got form one block. Sending puts your message at the top
// with room for the reply.

import { ActionBarPrimitive, MessagePrimitive, ThreadPrimitive, useAuiState, type TextMessagePartProps } from '@assistant-ui/react'
import { ArrowDown, Brain, Check, ChevronDown, Copy, Mail } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { Item } from '../events'
import type { Earlier } from '../useChat'
import { Markdown } from '../Markdown'
import { ItemRow } from '../rows'
import { ActivityRow } from './ActivityRow'
import { duration, useNow } from './motion'
import { useSmoothText } from './streaming'
import type { Away, UserMeta } from './toThread'
import { AttachmentChips } from '../AttachmentChips'

function UserText({ text }: TextMessagePartProps) {
  return <div className="ct-bubble">{text}</div>
}

function UserMessage() {
  const meta = useAuiState((s) => (s.message as unknown as { metadata?: { custom?: UserMeta } }).metadata?.custom)
  return (
    <MessagePrimitive.Root className={`ct-user${meta?.pending ? ' pending' : ''}`}>
      <MessagePrimitive.Parts components={{ Text: UserText }} />
      {meta?.deliveredTo && (
        <div className="ct-images" title="This chat runs in a terminal; cmdrd delivered the message as mail from the user">
          <Mail size={12} /> delivered by mail to {meta.deliveredTo}
        </div>
      )}
      <AttachmentChips list={meta?.attachments} />
    </MessagePrimitive.Root>
  )
}

function AssistantText({ text, status }: TextMessagePartProps) {
  const { shown, settled, animating } = useSmoothText(text, status.type === 'running')
  return (
    <div className="ct-text">
      <Markdown text={shown} streaming={animating} fadeAfter={animating ? settled : Infinity} />
    </div>
  )
}

function ItemPart({ data }: { data: Item }) {
  return (
    <div className="ct-item">
      <ItemRow item={data} />
    </div>
  )
}

// What mail set off while the user was away: one row that reads like the
// "Worked for" row, and the agent's report under it in a card that keeps the
// last finished report and fades to the next once it is complete.
function AwayPart({ data }: { data: Away }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="ct-away">
      <div className="ct-activity">
        <button type="button" className={`ct-activity-head${open ? ' open' : ''}`} onClick={() => setOpen(!open)} aria-expanded={open}>
          <Mail size={14} />
          <span className={data.running ? 'ct-shimmer' : ''}>While you were away</span>
          <span className="ct-elapsed">· {plural(data.mails, 'message')}</span>
          <ChevronDown size={14} className="ct-chevron" />
        </button>
        {open && (
          <div className="ct-away-body">
            {data.parts.map((p, i) => {
              const part = p as { type: string; text?: string; data?: unknown }
              if (part.type === 'text') return <div key={i} className="ct-text"><Markdown text={part.text ?? ''} /></div>
              if (part.type === 'data-activity') return <ActivityRow key={i} {...({ data: part.data } as Parameters<typeof ActivityRow>[0])} />
              if (part.type === 'data-item') return <ItemPart key={i} data={part.data as Item} />
              return null
            })}
          </div>
        )}
      </div>
      {data.report && (
        <section className="ct-report" aria-label="Since you were away">
          <div className="ct-report-kicker">Since you were away</div>
          <div key={data.report} className="ct-report-body ct-text">
            <Markdown text={reportBody(data.report)} />
          </div>
        </section>
      )}
    </div>
  )
}

// The card already says "Since you were away": drop the reply's own heading for it.
function reportBody(text: string) {
  return text.replace(/^\s*(#+\s*|\*\*)?since you were (away|gone)(\*\*)?:?\s*\n/i, '')
}

function plural(n: number, word: string) {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

// The reply's empty state, in the spot its content will fill: shown from the
// moment you send until the first step or word arrives.
function ThinkingIndicator() {
  const running = useAuiState((s) => (s.message as unknown as { status?: { type: string } }).status?.type === 'running')
  const startedAt = useAuiState((s) => {
    const messages = s.thread.messages as unknown as { role: string; createdAt?: Date }[]
    const index = (s.message as unknown as { index: number }).index
    return messages[index - 1]?.role === 'user' ? messages[index - 1].createdAt?.getTime() : undefined
  })
  const now = useNow(running)
  if (!running) return null
  return (
    <div className="ct-thinking" role="status" aria-label="Agent is working">
      <Brain size={14} className="ct-icon-swap" />
      <span className="ct-shimmer">Thinking</span>
      {startedAt !== undefined && <span className="ct-elapsed">· {duration(now - startedAt)}</span>}
    </div>
  )
}

function AssistantMessage() {
  return (
    <MessagePrimitive.Root className="ct-assistant">
      <MessagePrimitive.Parts
        unstable_showEmptyOnNonTextEnd={false}
        components={{
          Empty: ThinkingIndicator,
          Text: AssistantText,
          data: { by_name: { activity: ActivityRow, item: ItemPart, away: AwayPart } },
        }}
      />
      <MessagePrimitive.If last={false}>
        <ActionBarPrimitive.Root className="ct-actions">
          <ActionBarPrimitive.Copy className="ct-action" aria-label="Copy reply">
            <MessagePrimitive.If copied>
              <Check size={15} />
            </MessagePrimitive.If>
            <MessagePrimitive.If copied={false}>
              <Copy size={15} />
            </MessagePrimitive.If>
          </ActionBarPrimitive.Copy>
        </ActionBarPrimitive.Root>
      </MessagePrimitive.If>
    </MessagePrimitive.Root>
  )
}

const messageComponents = { UserMessage, AssistantMessage }

function usePairs() {
  const roles = useAuiState((s) => s.thread.messages.map((m) => m.role).join(','))
  const pairs: number[][] = []
  roles
    .split(',')
    .filter(Boolean)
    .forEach((role, index) => {
      if (role === 'user' || pairs.length === 0) pairs.push([index])
      else pairs[pairs.length - 1].push(index)
    })
  return pairs
}

// Long sessions hold hundreds of exchanges; only the latest render until you ask for more.
const PAIRS_SHOWN = 40

function PairedMessages({ tail, earlier }: { tail?: React.ReactNode; earlier?: Earlier }) {
  const all = usePairs()
  const [shown, setShown] = useState(PAIRS_SHOWN)
  const pairs = all.slice(-shown)
  const hidden = all.length - pairs.length
  const lastStart = pairs.at(-1)?.[0]
  const previousStart = useRef<number | undefined>(undefined)
  const container = useRef<HTMLDivElement>(null)

  // Older history is not loaded until you scroll to the top. Then a page
  // loads, and the view stays on what you were reading.
  const kept = useRef<{ height: number; top: number; version: number } | null>(null)
  const scroller = () => container.current?.closest('.ct-viewport') as HTMLElement | null
  const showEarlier = () => {
    const el = scroller()
    if (el) kept.current = { height: el.scrollHeight, top: el.scrollTop, version: earlier?.version ?? 0 }
    if (hidden > 0) setShown((n) => n + PAIRS_SHOWN)
    else earlier?.load()
  }
  const more = hidden > 0 || !!earlier?.has
  const top = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = top.current
    if (!el || !more || typeof IntersectionObserver === 'undefined') return
    const io = new IntersectionObserver((entries) => entries[0]?.isIntersecting && !earlier?.loading && showEarlier(), { root: scroller() })
    io.observe(el)
    return () => io.disconnect()
  }) // eslint-disable-line react-hooks/exhaustive-deps
  const restoring = useRef(false)
  useLayoutEffect(() => {
    const k = kept.current
    const el = scroller()
    if (!k || !el || (hidden === 0 && (earlier?.version ?? 0) === k.version)) return
    kept.current = null
    restoring.current = true
    el.scrollTop = el.scrollHeight - k.height + k.top
  })

  // Only a message you just sent (still optimistic) glides into place; a chat
  // opening, whose history lands in several batches, jumps straight to its latest pair.
  const justSent = useAuiState((s) => {
    const m = lastStart === undefined ? undefined : (s.thread.messages[lastStart] as unknown as { metadata?: { custom?: UserMeta } } | undefined)
    return !!m?.metadata?.custom?.pending
  })

  // Opening a chat (or a pair arriving that you did not just send) lands at the
  // very bottom; the last pair is at least one view tall, so a short exchange
  // still shows your message at the top. A message you just sent glides up to
  // the top of the view, its reply growing below it.
  useLayoutEffect(() => {
    if (lastStart === undefined || lastStart === previousStart.current) return
    if (restoring.current) {
      // Earlier messages went in above: the latest pair is not new.
      restoring.current = false
      previousStart.current = lastStart
      return
    }
    const first = previousStart.current === undefined || !justSent
    previousStart.current = lastStart
    const scroller = container.current?.closest('.ct-viewport') as HTMLElement | null
    const pair = container.current?.querySelector('[data-pair]:last-of-type') as HTMLElement | null
    if (!scroller || !pair) return
    if (first) {
      scroller.scrollTo?.({ top: scroller.scrollHeight, behavior: 'instant' })
      return
    }
    const top = pair.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop
    scroller.scrollTo?.({ top, behavior: 'smooth' })
  }, [lastStart, justSent])

  return (
    <div ref={container}>
      {more && (
        <div ref={top}>
          <button type="button" className="ct-earlier" onClick={showEarlier} disabled={earlier?.loading}>
            {earlier?.loading ? 'Loading earlier messages…' : 'Show earlier messages'}
          </button>
        </div>
      )}
      {pairs.map((indices, i) => (
        <section key={indices[0]} data-pair className={`ct-pair${i === pairs.length - 1 ? ' last' : ''}`}>
          {indices.map((index) => (
            <ThreadPrimitive.MessageByIndex key={index} index={index} components={messageComponents} />
          ))}
          {i === pairs.length - 1 && tail}
        </section>
      ))}
    </div>
  )
}

/**
 * Thread renders the conversation; the composer lives outside it (Commander's
 * own). tail ends the latest exchange: what the agent is doing between turns.
 */
export function Thread({ empty, earlier, tail }: { empty: React.ReactNode; earlier?: Earlier; tail?: React.ReactNode }) {
  return (
    <ThreadPrimitive.Root className="ct-root">
      <ThreadPrimitive.Empty>
        <div className="ct-empty">{empty}</div>
      </ThreadPrimitive.Empty>
      <ThreadPrimitive.If empty={false}>
        <ThreadPrimitive.ViewportProvider>
          <ThreadPrimitive.Viewport autoScroll={false} scrollToBottomOnRunStart={false} scrollToBottomOnInitialize={false} scrollToBottomOnThreadSwitch={false} className="ct-viewport">
            <div className="ct-column">
              <PairedMessages earlier={earlier} tail={tail} />
            </div>
          </ThreadPrimitive.Viewport>
          <div className="ct-jump-wrap">
            <ThreadPrimitive.ScrollToBottom behavior="instant" className="ct-jump" aria-label="Jump to latest">
              <ArrowDown size={15} />
            </ThreadPrimitive.ScrollToBottom>
          </div>
        </ThreadPrimitive.ViewportProvider>
      </ThreadPrimitive.If>
    </ThreadPrimitive.Root>
  )
}
