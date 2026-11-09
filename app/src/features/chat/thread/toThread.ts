// ChatState (cmdrd's events, reduced) as assistant-ui messages. Each message
// you send opens a pair; everything the agent does until the next one is a
// single assistant message whose parts are its text, one activity row per run
// of thinking and tool calls, and the cards that need you (approvals,
// questions, plans) or tell you something (mail, hooks, notices).

import type { ThreadMessageLike } from '@assistant-ui/react'
import type { ChatState, Item, ItemAttachment, ThinkingItem, ToolItem, UserItem } from '../events'
import { parseMail } from '../mail'

type Part = Exclude<ThreadMessageLike['content'], string>[number]

export type Step = ThinkingItem | ToolItem

export interface Activity {
  steps: Step[]
  active: boolean
  startedAt?: number
  endedAt?: number
}

export interface UserMeta {
  queued?: boolean
  pending?: boolean
  attachments?: ItemAttachment[]
  deliveredTo?: string // reached a terminal chat by mail, not this stream
}

// Shown on the tool's own step instead (or by the card that replaced it).
const HIDDEN_TOOLS = new Set(['AskUserQuestion', 'ExitPlanMode'])

interface Reply {
  id: string
  createdAt: Date
  parts: Part[]
  steps: Step[]
  startedAt?: number // when the message it answers was sent
  firstGroup: boolean
}

function userMessage(it: UserItem, id: string, queued: boolean): ThreadMessageLike {
  const meta: UserMeta = { queued, pending: it.pending, attachments: it.attachments, deliveredTo: it.deliveredVia === 'mail' ? it.deliveredTo : undefined }
  return {
    role: 'user',
    id,
    createdAt: it.ts ? new Date(it.ts) : undefined,
    content: [{ type: 'text', text: it.text }],
    metadata: { custom: meta as Record<string, unknown> },
  }
}

function stepStart(step: Step): number | undefined {
  return step.ts
}

function stepEnd(step: Step): number | undefined {
  if (step.type === 'tool') return step.endedAt ?? step.ts
  return step.ts !== undefined && step.durationMs ? step.ts + step.durationMs : step.ts
}

function flushSteps(r: Reply, active: boolean) {
  if (!r.steps.length) return
  const starts = r.steps.map(stepStart).filter((t): t is number => t !== undefined)
  const ends = r.steps.map(stepEnd).filter((t): t is number => t !== undefined)
  const data: Activity = {
    steps: r.steps,
    active,
    startedAt: r.firstGroup && r.startedAt !== undefined ? r.startedAt : starts[0],
    endedAt: ends.length ? Math.max(...ends) : undefined,
  }
  r.parts.push({ type: 'data-activity', id: `activity-${r.steps[0].id}`, data } as unknown as Part)
  r.steps = []
  r.firstGroup = false
}

function itemPart(it: Item): Part {
  return { type: 'data-item', id: `item-${it.id}`, data: it } as unknown as Part
}

function mailCount(text: string): number {
  const m = parseMail(text)
  return Math.max(1, m.mails.length + m.more)
}

/** Away is the work mail started while the user was not talking: folded into one row. */
export interface Away {
  mails: number // messages that came in
  running: boolean // still working on them
  parts: Part[] // everything folded, in order, for when the row is opened
  report?: string // the agent's last finished reply: the "since you were away" report
}

/**
 * foldAway folds what mail set off after the agent answered the user: from
 * the first mail on, the mail rows, the work and the replies go into one
 * "while you were away" row, and the last finished reply becomes its report.
 * While a turn still runs, the report is the reply before it, so the report
 * only changes once the new one is complete. A reply with no mail is unchanged.
 */
export function foldAway(parts: Part[], id: string, running: boolean): Part[] {
  const type = (p: Part) => (p as { type: string }).type
  const isMail = (p: Part) => type(p) === 'data-item' && (p as { data: Item }).data.type === 'system' && ((p as { data: Item }).data as { variant?: string }).variant === 'mail'
  const first = parts.findIndex(isMail)
  if (first < 0) return parts
  const tail = parts.slice(first)
  // Running: the newest turn (from the last mail on) is still being written.
  const lastMail = tail.map(isMail).lastIndexOf(true)
  const settled = running ? tail.slice(0, lastMail) : tail
  let at = -1
  for (let i = settled.length - 1; i >= 0; i--) {
    if (type(settled[i]) === 'text') {
      at = i
      break
    }
  }
  const away: Away = {
    // A delivery can carry many messages ("14 messages from ..."): count the messages.
    mails: tail.filter(isMail).reduce((n, p) => n + mailCount((p as { data: { text?: string } }).data.text ?? ''), 0),
    running,
    parts: tail.filter((_, i) => i !== at),
    report: at >= 0 ? (tail[at] as { text: string }).text : undefined,
  }
  return [...parts.slice(0, first), { type: 'data-away', id: `away-${id}`, data: away } as unknown as Part]
}

/** toThread converts the reduced chat into assistant-ui messages; `running` is whether a turn is in progress. */
export function toThread(s: ChatState, running: boolean): ThreadMessageLike[] {
  // Questions and plans replace the tool call that asked them.
  const requestTools = new Set<string>()
  for (const id of s.order) {
    const it = s.items[id]
    if ((it?.type === 'question' || it?.type === 'plan') && it.toolUseId) requestTools.add(it.toolUseId)
  }

  const out: ThreadMessageLike[] = []
  let reply: Reply | null = null
  let users = 0
  let lastUserAt: number | undefined
  const seenNotices = new Set<string>()

  // Message ids are positional, so an optimistic message keeps its id when
  // cmdrd's copy replaces it, and a reply keeps its id from first token to last.
  const openReply = (ts?: number): Reply => {
    reply ??= { id: `reply-${users}`, createdAt: new Date(ts ?? lastUserAt ?? 0), parts: [], steps: [], startedAt: lastUserAt, firstGroup: true }
    return reply
  }
  const closeReply = () => {
    if (!reply) return
    flushSteps(reply, false)
    out.push({ role: 'assistant', id: reply.id, createdAt: reply.createdAt, content: foldAway(reply.parts, reply.id, false), status: { type: 'complete', reason: 'stop' } })
    reply = null
  }

  for (const id of s.order) {
    const it = s.items[id]
    if (!it) continue
    switch (it.type) {
      case 'user':
        // Sent mid-turn and not read yet: it waits above the composer, and
        // joins the thread where the agent reads it (prompt_read).
        if (it.queued && running) break
        closeReply()
        out.push(userMessage(it, `user-${users++}`, false))
        lastUserAt = it.ts
        break
      case 'text':
        if (!it.text) break
        flushSteps(openReply(it.ts), false)
        openReply().parts.push({ type: 'text', text: it.text })
        break
      case 'thinking':
        if (it.done && !it.text && !it.durationMs && !it.tokens) break
        openReply(it.ts).steps.push(it)
        break
      case 'tool':
        if (HIDDEN_TOOLS.has(it.name) && requestTools.has(it.id)) break
        openReply(it.ts).steps.push(it)
        break
      case 'permission':
        // A resolved approval shows as its tool step's state.
        if (it.resolved && it.toolUseId && s.items[it.toolUseId]) break
        flushSteps(openReply(it.ts), false)
        openReply().parts.push(itemPart(it))
        break
      case 'system':
        // The same notice repeats on every restart (e.g. the CLI version warning); once is enough.
        if ((it.variant === 'notice' || it.variant === 'warn') && seenNotices.has(it.text)) break
        seenNotices.add(it.text)
        flushSteps(openReply(it.ts), false)
        openReply().parts.push(itemPart(it))
        if (it.variant === 'mail') {
          // Mail wakes the agent: the work after it is timed from the mail, not from the user's last message.
          const r = openReply()
          r.startedAt = it.ts
          r.firstGroup = true
        }
        break
      case 'question':
      case 'plan':
        flushSteps(openReply(it.ts), false)
        openReply().parts.push(itemPart(it))
        break
      case 'turn': {
        const stopped = it.terminalReason === 'aborted_streaming' || it.terminalReason === 'aborted_tools'
        if (it.isError || stopped) {
          const open = openReply(it.ts)
          flushSteps(open, false)
          open.parts.push(itemPart(it))
        }
        break
      }
    }
  }

  // `as`: reply is assigned inside openReply, which TypeScript's narrowing cannot see.
  const r = reply as Reply | null
  if (r) {
    const last = r.parts[r.parts.length - 1]
    // The trailing run of steps is live while the turn runs and nothing came after it.
    flushSteps(r, running && (!last || r.steps.length > 0))
    out.push({ role: 'assistant', id: r.id, createdAt: r.createdAt, content: foldAway(r.parts, r.id, running), status: running ? { type: 'running' } : { type: 'complete', reason: 'stop' } })
  } else if (running && out.length && out[out.length - 1].role === 'user') {
    // Sent, nothing back yet: an empty running reply shows the thinking state.
    out.push({ role: 'assistant', id: `reply-${users}`, content: [], status: { type: 'running' } })
  }

  return out
}
