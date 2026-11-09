// While the user talks to an agent its mail waits (cmdrd's hold: two minutes after
// his last message or keystroke), then goes in as one batch. These tell cmdrd
// he is typing, or to let the waiting mail in now.

import { useStore } from './store'

const TYPING_EVERY_MS = 20_000 // well inside the hold, so it never lapses while he types
const lastTyping = new Map<string, number>()

function post(role: string, action: 'typing' | 'mail-now') {
  const cfg = useStore.getState().config
  if (!cfg || cfg.demo) return Promise.resolve()
  return fetch(`${cfg.apiUrl}/api/chat/${encodeURIComponent(role)}/${action}`, { method: 'POST', headers: { Authorization: `Bearer ${cfg.token}` } }).then(
    () => undefined,
    () => undefined, // best effort: the hold lapses on its own
  )
}

/** typing keeps role's mail waiting while the user types to it (throttled). */
export function typing(role: string) {
  const now = Date.now()
  if (now - (lastTyping.get(role) ?? 0) < TYPING_EVERY_MS) return
  lastTyping.set(role, now)
  void post(role, 'typing')
}

/** mailNow lets role's waiting mail in at once, as one batch. */
export function mailNow(role: string) {
  lastTyping.delete(role)
  return post(role, 'mail-now')
}

export interface WaitingMail {
  id: string
  from: string
  subject: string
  preview: string
  sent_at: string
}

const DEMO_MAIL: WaitingMail[] = [
  { id: '1', from: 'manager-commander-il9', subject: 'Operator il9.15.25 is ready for review', preview: 'Branch op/app-composer is ff-able; tests green.', sent_at: new Date(Date.now() - 4 * 60_000).toISOString() },
  { id: '2', from: 'commander', subject: 'CI finished on the sidebar PR', preview: 'All checks passed.', sent_at: new Date(Date.now() - 45_000).toISOString() },
]

/** waitingMail lists the mail waiting for role (oldest first); [] when cmdrd cannot say. */
export async function waitingMail(role: string): Promise<WaitingMail[]> {
  const cfg = useStore.getState().config
  if (!cfg) return []
  if (cfg.demo) return DEMO_MAIL
  try {
    const res = await fetch(`${cfg.apiUrl}/api/chat/${encodeURIComponent(role)}/mail`, { headers: { Authorization: `Bearer ${cfg.token}` } })
    if (!res.ok) return []
    return ((await res.json()) as { mail?: WaitingMail[] }).mail ?? []
  } catch {
    return []
  }
}
