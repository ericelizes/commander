// Mail waiting for the agent in this chat, a small pill in the input bar:
// "2 waiting". Hovering lists the messages (from which workstream, what, when); clicking the
// pill has the agent read them now (otherwise they go in when the user pauses).
// An agent in the middle of a step reads them when the step ends, so after a
// click the pill says that instead of nothing changing.

import { useEffect, useRef, useState } from 'react'
import { mailNow, waitingMail, type WaitingMail } from '../mailHold'
import { useStore } from '../store'
import { fmtAge, roleLabel } from '../viewmodel'

const CLOSE_DELAY_MS = 150 // time to move from the pill into the popup

function sentAgo(iso: string): string {
  const secs = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000))
  return secs < 60 ? 'just now' : `${fmtAge(secs)} ago`
}

export function MailChip({ role }: { role: string }) {
  const n = useStore((s) => s.snapshot?.unread?.[role] ?? 0)
  const snapshot = useStore((s) => s.snapshot)
  const [released, setReleased] = useState(0) // the count when the user clicked
  const [open, setOpen] = useState(false)
  const [mail, setMail] = useState<WaitingMail[] | null>(null)
  const [expanded, setExpanded] = useState<string | null>(null)
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  useEffect(() => () => clearTimeout(closeTimer.current), [])
  useEffect(() => {
    if (open) void waitingMail(role).then(setMail)
  }, [open, role, n])

  if (!n) return null
  if (released && n <= released) {
    return (
      <span className="cc-mail-pill done" role="status">
        Sending {n} to the agent…
      </span>
    )
  }
  const show = () => {
    clearTimeout(closeTimer.current)
    setOpen(true)
  }
  const hide = () => {
    clearTimeout(closeTimer.current)
    closeTimer.current = setTimeout(() => setOpen(false), CLOSE_DELAY_MS)
  }
  const label = `${n} waiting`
  return (
    <span className="cc-mail-wrap" onMouseEnter={show} onMouseLeave={hide} onFocus={show} onBlur={hide}>
      <button
        type="button"
        className="cc-mail-pill"
        aria-label={`${label}: send them now`}
        aria-expanded={open}
        onClick={() => {
          setReleased(n)
          setOpen(false)
          void mailNow(role)
        }}
      >
        {label}
      </button>
      {open && (
        <div className="cc-mail-pop" role="dialog" aria-label="Waiting messages">
          <ul>
            {(mail ?? []).map((m) => (
              <li key={m.id}>
                <button type="button" className={`cc-mail-row${expanded === m.id ? ' open' : ''}`} onClick={() => setExpanded(expanded === m.id ? null : m.id)}>
                  <span className="cc-mail-who">{roleLabel(snapshot, m.from)}</span>
                  <span className="cc-mail-at">{sentAgo(m.sent_at)}</span>
                  <span className="cc-mail-what">{m.subject || m.preview}</span>
                  {expanded === m.id && m.subject && m.preview && <span className="cc-mail-preview">{m.preview}</span>}
                </button>
              </li>
            ))}
            {mail && mail.length === 0 && <li className="cc-mail-empty">{n} message{n > 1 ? 's' : ''} for {role}</li>}
            {!mail && <li className="cc-mail-empty">Loading…</li>}
          </ul>
          <div className="cc-mail-foot">They go in as one when you pause. Click the pill to send them now.</div>
        </div>
      )}
    </span>
  )
}
