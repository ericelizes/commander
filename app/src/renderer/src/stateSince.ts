// When each watch item (a Manager's monitoring list) was first seen, for
// items cmdrd does not time itself. Board rows are timed by cmdrd (display
// since). Kept in this viewer's storage so a reload does not restart the
// clock; it starts when the app first sees the item.

const KEY = 'cmdr.stateSince'

type Seen = Record<string, { state: string; since: number }>

function load(): Seen {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}') as Seen
  } catch {
    return {}
  }
}

let seen: Seen | null = null

/** trackedSince is when row `key` started showing `state` (unix s), as this app saw it. */
export function trackedSince(key: string, state: string, nowSec: number): number {
  seen ??= load()
  const e = seen[key]
  if (e?.state === state) return e.since
  seen[key] = { state, since: nowSec }
  try {
    localStorage.setItem(KEY, JSON.stringify(seen))
  } catch {
    // no storage (private window): the clock lives for this session only
  }
  return nowSec
}

/** resetTracked forgets every row (tests). */
export function resetTracked() {
  seen = {}
}
