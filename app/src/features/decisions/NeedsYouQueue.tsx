import { splitTickets } from '../linear/Ticket'
import { useEffect, useId, useMemo, useState } from 'react'
import type { KeyboardEvent } from 'react'
import { askWhere, formatAge, itemKey, itemQuestion, resolveNeeds } from './logic'
import type { CommanderApi, NeedsYouItem } from './types'
import './decisions.css'

export interface NeedsYouQueueProps {
  // snapshot.needs_you, in the order cmdrd sends it (workstream priority).
  items: NeedsYouItem[]
  api: CommanderApi
  // Enter or click: open the item's card (the host shows <DecisionCard>).
  onOpen: (item: NeedsYouItem) => void
  // Current time in ms, for ages (default Date.now(); pass it to keep ages ticking).
  now?: number
  autoFocus?: boolean
  // The item the host shows right now (itemKey); drawn as selected.
  selectedKey?: string
  // With onToggle the head is a button that folds the list away; collapsed hides it.
  collapsed?: boolean
  onToggle?: () => void
}

type Busy = 'approve' | 'defer'

// The fleet-wide NEEDS YOU list. Plain flags (no decision card) can be
// approved with a or deferred with d right here; cards open with Enter.
export function NeedsYouQueue({ items, api, onOpen, now, autoFocus, selectedKey, collapsed, onToggle }: NeedsYouQueueProps) {
  const [activeKey, setActiveKey] = useState<string | null>(null)
  const [busy, setBusy] = useState<Record<string, Busy>>({})
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [resolved, setResolved] = useState<Set<string>>(() => new Set())
  const [announce, setAnnounce] = useState('')
  const ids = useId()

  // Items resolved here stay hidden until the next snapshot drops them.
  const visible = useMemo(() => items.filter(i => !resolved.has(itemKey(i))), [items, resolved])
  useEffect(() => {
    const live = new Set(items.map(itemKey))
    setResolved(prev => {
      const next = new Set([...prev].filter(k => live.has(k)))
      return next.size === prev.size ? prev : next
    })
  }, [items])

  let active = visible.findIndex(i => itemKey(i) === activeKey)
  if (active < 0 && visible.length > 0) active = 0
  const current = active >= 0 ? visible[active] : undefined
  const nowMs = now ?? Date.now()

  function move(i: number) {
    const next = visible[Math.max(0, Math.min(visible.length - 1, i))]
    if (!next) return
    setActiveKey(itemKey(next))
    document.getElementById(optionId(next))?.scrollIntoView?.({ block: 'nearest' })
  }

  function optionId(item: NeedsYouItem) {
    return `${ids}-${itemKey(item).replace(/[^\w-]/g, '_')}`
  }

  async function quick(item: NeedsYouItem, action: Busy) {
    const key = itemKey(item)
    if (busy[key]) return
    if (item.decision) {
      setAnnounce('This one has a decision card: press Enter to open it.')
      return
    }
    setBusy(b => ({ ...b, [key]: action }))
    setErrors(e => ({ ...e, [key]: '' }))
    const res = await resolveNeeds(api, item, action)
    setBusy(b => {
      const { [key]: _, ...rest } = b
      return rest
    })
    if (res.status === 'error') {
      setErrors(e => ({ ...e, [key]: res.message }))
      setAnnounce(`Could not ${action}: ${res.message}`)
      return
    }
    // Keep the selection on the row that slides into this one's place.
    const idx = visible.findIndex(i => itemKey(i) === key)
    const after = visible[idx + 1] ?? visible[idx - 1]
    if (after) setActiveKey(itemKey(after))
    setResolved(prev => new Set(prev).add(key))
    setAnnounce(res.status === 'gone'
      ? `Already answered: ${itemQuestion(item)}`
      : `${action === 'approve' ? 'Approved' : 'Deferred'}: ${itemQuestion(item)}`)
  }

  function onKeyDown(e: KeyboardEvent<HTMLUListElement>) {
    if (e.metaKey || e.ctrlKey || e.altKey) return
    switch (e.key) {
      case 'ArrowDown':
      case 'j':
        e.preventDefault()
        move(active + 1)
        return
      case 'ArrowUp':
      case 'k':
        e.preventDefault()
        move(active - 1)
        return
      case 'Home':
        e.preventDefault()
        move(0)
        return
      case 'End':
        e.preventDefault()
        move(visible.length - 1)
        return
      case 'Enter':
        if (current) {
          e.preventDefault()
          onOpen(current)
        }
        return
      case 'a':
        if (current) {
          e.preventDefault()
          quick(current, 'approve')
        }
        return
      case 'd':
        if (current) {
          e.preventDefault()
          quick(current, 'defer')
        }
    }
  }

  const headId = `${ids}-head`
  return (
    <div className="cmdr-nyq">
      {onToggle ? (
        <button type="button" className={`nyq-head nyq-toggle${collapsed ? ' collapsed' : ''}`} id={headId} onClick={onToggle} aria-expanded={!collapsed}>
          <span>⚑ Needs you</span>
          <NeedsYouBadge count={visible.length} />
          <span className="nyq-chevron" aria-hidden="true">▾</span>
        </button>
      ) : (
        <div className="nyq-head" id={headId}>
          <span>⚑ Needs you</span>
          <NeedsYouBadge count={visible.length} />
        </div>
      )}
      {collapsed ? null : visible.length === 0
        ? (
          <div className="nyq-empty">
            <strong>All clear</strong>
            Nothing is waiting on you.
          </div>
          )
        : (
          <ul
            role="listbox"
            aria-labelledby={headId}
            aria-activedescendant={current ? optionId(current) : undefined}
            tabIndex={0}
            autoFocus={autoFocus}
            className="nyq-list"
            onKeyDown={onKeyDown}
          >
            {visible.map((item, i) => {
              const key = itemKey(item)
              const age = formatAge(item.since, nowMs)
              return (
                <li
                  key={key}
                  id={optionId(item)}
                  role="option"
                  aria-selected={i === active}
                  aria-busy={busy[key] ? true : undefined}
                  className={`nyq-item${i === active ? ' is-active' : ''}${key === selectedKey ? ' is-selected' : ''}${busy[key] ? ' is-busy' : ''}`}
                  onClick={() => {
                    setActiveKey(key)
                    onOpen(item)
                  }}
                >
                  <span className="nyq-q">{splitTickets(itemQuestion(item)).text}</span>
                  {age
                    ? <time className="nyq-age" dateTime={new Date(item.since! * 1000).toISOString()} title={`raised ${age === 'now' ? 'just now' : age + ' ago'}`}>{age}</time>
                    : <span />}
                  <span className="nyq-meta">
                    <span className="nyq-rank" title={`Workstream ${item.rank}${item.track_letter ?? ''}`}>{item.rank}{item.track_letter ?? ''}</span>
                    <span className="nyq-ws">{splitTickets(askWhere(item)).text}</span>
                    {busy[key] && <span>· {busy[key] === 'approve' ? 'approving…' : 'deferring…'}</span>}
                  </span>
                  {item.stale_why && <span className="nyq-warning">{item.stale_why}</span>}
                  {errors[key] && <span className="nyq-err">✗ {errors[key]}</span>}
                </li>
              )
            })}
          </ul>
          )}
      {!collapsed && visible.length > 0 && (
        <div className="nyq-hints" aria-hidden="true">
          <span><kbd>Enter</kbd>open</span>
          <span><kbd>a</kbd>approve</span>
          <span><kbd>d</kbd>defer</span>
          <span><kbd>↑↓</kbd>move</span>
        </div>
      )}
      <div role="status" aria-live="polite" className="nyq-sr">{announce}</div>
    </div>
  )
}

// The sidebar count: yellow only when something needs the user.
export function NeedsYouBadge({ count }: { count: number }) {
  if (count <= 0) return null
  return (
    <span className="cmdr-nyq-badge" aria-label={`${count} waiting on you`}>
      {count > 99 ? '99+' : count}
    </span>
  )
}
