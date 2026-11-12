import { ChevronDown, Mail as MailIcon } from 'lucide-react'
import { memo, useContext, useState } from 'react'
import { fmtCost, fmtDuration, fmtTokens, type Item, type SystemItem, type TextItem, type ThinkingItem, type TurnItem, type UserItem } from './events'
import { Icon, Spinner } from './icons'
import { CopyButton, copyableMarkdown, Markdown } from './Markdown'
import { AttachmentChips } from './AttachmentChips'
import { ToolCard } from './ToolCard'
import { PlanCard, PermissionCard, QuestionCard } from './Approvals'
import { useItems } from './context'
import { ChatHostContext } from './host'
import { parseMail, type Mail } from './mail'

export function fmtTime(ts?: number): string {
  if (!ts) return ''
  return new Date(ts).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
}

function Stamp({ ts }: { ts?: number }) {
  if (!ts) return null
  return (
    <time className="cc-stamp" dateTime={new Date(ts).toISOString()} title={new Date(ts).toLocaleString()}>
      {fmtTime(ts)}
    </time>
  )
}

const UserBubble = memo(function UserBubble({ item }: { item: UserItem }) {
  return (
    <div className={`cc-row cc-user-row${item.pending ? ' cc-pending' : ''}`}>
      <Stamp ts={item.ts} />
      <div className="cc-user">
        {item.queued && <span className="cc-queued-tag">Queued</span>}
        <div className="cc-user-text">{item.text}</div>
        {item.deliveredVia === 'mail' && (
          <div className="cc-delivered" title="This chat runs in a terminal; cmdrd delivered the message as mail from the user">
            <Icon name="mail" size={11} /> delivered by mail to {item.deliveredTo}
          </div>
        )}
        <AttachmentChips list={item.attachments} />
      </div>
    </div>
  )
})

const AssistantText = memo(function AssistantText({ item, nested }: { item: TextItem; nested?: boolean }) {
  return (
    <div className={`cc-row cc-assistant-row${nested ? ' cc-nested-text' : ''}`}>
      <Markdown text={item.text} streaming={item.streaming} />
      {!item.streaming && !nested && (
        <div className="cc-row-actions">
          <Stamp ts={item.ts} />
          <CopyButton text={copyableMarkdown(item.text)} />
        </div>
      )}
    </div>
  )
})

const ThinkingRow = memo(function ThinkingRow({ item }: { item: ThinkingItem }) {
  const [open, setOpen] = useState(false)
  const label = item.done ? `Thought${item.durationMs ? ` for ${fmtDuration(item.durationMs)}` : ''}` : 'Thinking'
  const hasText = item.text.trim().length > 0
  return (
    <div className="cc-row cc-thinking">
      <button type="button" className="cc-thinking-head" disabled={!hasText} aria-expanded={open} onClick={() => setOpen(!open)}>
        {item.done ? <Icon name="brain" size={13} /> : <Spinner size={12} />}
        <span className={item.done ? '' : 'cc-shimmer'}>{label}</span>
        {item.tokens ? <span className="cc-muted">· ~{fmtTokens(item.tokens)} tokens</span> : null}
        {hasText && <Icon name="chevron" size={11} className={`cc-chevron${open ? ' cc-open' : ''}`} />}
      </button>
      {open && hasText && <div className="cc-thinking-text">{item.text}</div>}
    </div>
  )
})

const sysIcon = { mail: 'mail', hook: 'zap', notice: 'info', warn: 'alert', error: 'alert', compact: 'layers', interrupted: 'stop', exit: 'stop', summary: 'layers', command: 'terminal' } as const

// What the user calls a sender, without the app: the role with its prefix as words.
function plainName(role: string): string {
  if (role === 'user') return 'You'
  if (role === 'commander') return 'Commander'
  if (role === 'cmdrd') return 'Commander'
  if (role.startsWith('manager-')) return `Manager ${role.slice(8)}`
  if (role.startsWith('operator-')) return 'an Operator'
  return role || 'an agent'
}

/**
 * MailRow is mail that reached the agent, as quiet as its activity row: who
 * wrote and about what, the body one click away.
 */
function MailRow({ mail }: { mail: Mail }) {
  const [open, setOpen] = useState(false)
  const { agentName } = useContext(ChatHostContext)
  const name = agentName?.(mail.from) ?? plainName(mail.from)
  return (
    <div className="cc-row cc-mail">
      <div className={`cc-mail-head${open ? ' open' : ''}`}>
        <MailIcon size={14} aria-hidden />
        <span className="cc-mail-from">Message from {name}</span>
        {mail.body ? (
          <button type="button" className="cc-mail-subject" onClick={() => setOpen(!open)} aria-expanded={open}>
            <span>{mail.subject}</span>
            <ChevronDown size={14} className="ct-chevron" />
          </button>
        ) : (
          <span className="cc-mail-subject">
            <span>{mail.subject}</span>
          </span>
        )}
      </div>
      {open && mail.body && <div className="cc-mail-body">{mail.body}</div>}
    </div>
  )
}

/** MailBatchRow is several messages that arrived together: one row, the list under it. */
function MailBatchRow({ mails, more }: { mails: Mail[]; more: number }) {
  const [open, setOpen] = useState(false)
  const { agentName } = useContext(ChatHostContext)
  const names = [...new Set(mails.map((m) => agentName?.(m.from) ?? plainName(m.from)))]
  const n = mails.length + more
  return (
    <div className="cc-row cc-mail">
      <div className={`cc-mail-head${open ? ' open' : ''}`}>
        <MailIcon size={14} aria-hidden />
        <span className="cc-mail-from">{n} messages</span>
        <button type="button" className="cc-mail-subject" onClick={() => setOpen(!open)} aria-expanded={open}>
          <span>from {names.join(', ')}</span>
          <ChevronDown size={14} className="ct-chevron" />
        </button>
      </div>
      {open && (
        <div className="cc-mail-batch">
          {mails.map((m, i) => (
            <MailRow key={i} mail={m} />
          ))}
          {more > 0 && <div className="cc-row cc-mail cc-mail-more">and {more} more unread</div>}
        </div>
      )}
    </div>
  )
}

const SystemRow = memo(function SystemRow({ item }: { item: SystemItem }) {
  const [open, setOpen] = useState(false)
  if (item.variant === 'mail') {
    const { mails, more } = parseMail(item.text)
    // One delivery is one row, however many messages it carries.
    if (mails.length > 1 || more > 0) return <MailBatchRow mails={mails} more={more} />
    return mails.length ? <MailRow mail={mails[0]} /> : null
  }
  const long = item.variant === 'command' || item.text.length > 160
  if (item.variant === 'summary') {
    // The summary Claude reads after compacting: one quiet line under the compaction divider.
    return (
      <div className="cc-row cc-summary">
        <button type="button" className="cc-summary-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
          <Icon name="layers" size={11} /> {open ? 'Hide' : 'Read'} the summary of the earlier conversation
        </button>
        {open && <div className="cc-summary-text">{item.detail}</div>}
      </div>
    )
  }
  if (item.variant === 'compact' || item.variant === 'interrupted') {
    return (
      <div className={`cc-row cc-divider cc-divider-${item.variant}`} title={item.ts ? new Date(item.ts).toLocaleString() : undefined}>
        <span className="cc-divider-label">
          <Icon name={sysIcon[item.variant]} size={11} /> {item.text}
          {item.detail && <span className="cc-muted"> · {item.detail}</span>}
        </span>
      </div>
    )
  }
  return (
    <div className={`cc-row cc-system cc-system-${item.variant}`}>
      <Stamp ts={item.ts} />
      <div className="cc-system-card">
        <Icon name={sysIcon[item.variant]} size={13} />
        <div className="cc-system-body">
          {item.variant === 'hook' && <span className="cc-system-kind">Hook</span>}
          {long && !open ? (
            <button type="button" className="cc-linkish" onClick={() => setOpen(true)}>
              {item.text.split('\n')[0].slice(0, 140)} <span className="cc-muted">· show</span>
            </button>
          ) : (
            <span className="cc-system-text">{item.text}</span>
          )}
        </div>
      </div>
    </div>
  )
})

const TurnFooter = memo(function TurnFooter({ item }: { item: TurnItem }) {
  const parts: string[] = []
  if (item.durationMs) parts.push(fmtDuration(item.durationMs))
  if (item.outputTokens) parts.push(`${fmtTokens(item.outputTokens)} out`)
  if (item.costDelta) parts.push(fmtCost(item.costDelta))
  const stopped = item.terminalReason === 'aborted_streaming' || item.terminalReason === 'aborted_tools'
  if (stopped) {
    // One marker for a stopped turn: the same "Interrupted" divider the stop
    // itself draws, or nothing when that divider is already there.
    if (item.shownAsInterrupted) return null
    return (
      <div className="cc-row cc-divider cc-divider-interrupted" title={parts.join(' · ') || undefined}>
        <span className="cc-divider-label">
          <Icon name={sysIcon.interrupted} size={11} /> Interrupted
        </span>
      </div>
    )
  }
  return (
    <div className={`cc-row cc-turn${item.isError && !stopped ? ' cc-turn-error' : ''}`}>
      <span>
        {stopped ? 'Stopped' : item.isError ? `Turn failed (${item.subtype})` : 'Done'}
        {parts.length > 0 && ` · ${parts.join(' · ')}`}
      </span>
    </div>
  )
})

export function ItemRow({ item, nested }: { item: Item; nested?: boolean }) {
  switch (item.type) {
    case 'user':
      return <UserBubble item={item} />
    case 'text':
      return <AssistantText item={item} nested={nested} />
    case 'thinking':
      return <ThinkingRow item={item} />
    case 'tool':
      return <ToolCard item={item} nested={nested} />
    case 'system':
      return <SystemRow item={item} />
    case 'turn':
      return nested ? null : <TurnFooter item={item} />
    case 'permission':
      return <PermissionCard item={item} />
    case 'question':
      return <QuestionCard item={item} />
    case 'plan':
      return <PlanCard item={item} />
  }
}

export function ItemView({ id, nested }: { id: string; nested?: boolean }) {
  const items = useItems()
  const it = items[id]
  if (!it) return null
  return <ItemRow item={it} nested={nested} />
}
