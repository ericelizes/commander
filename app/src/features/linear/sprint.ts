import { create } from 'zustand'
import { useEffect } from 'react'
import { useStore } from '../../renderer/src/store'

export interface LinearIssue { id: string; team?: string; title: string; status_name: string; status_type: string; url: string; added_at?: string; completed_at?: string }
export interface Cycle { name: string; start?: string; end?: string }
export interface TeamSprint { team: string; state: string; cycle?: Cycle | null; fetched_at?: string; stale_since?: string }
export interface Sprint { teams?: TeamSprint[]; state: string; cycle?: Cycle | null; key: 'saved' | 'not set'; issues: LinearIssue[]; fetched_at?: string; stale_since?: string }
export const useSprint = create<{ data: Sprint | null }>(() => ({ data: null }))

const reads = new WeakMap<object, { last: number; pending?: Promise<Sprint | null> }>()

const refresh = new EventTarget()
export function refreshLinearSprint(config: object) {
  const entry = reads.get(config)
  if (entry) entry.last = -Infinity
  refresh.dispatchEvent(new Event('refresh'))
}

// One lazy read every 30s while the app is visible; cmdrd caches each merged read for 30s.
export function useLinearSprint() {
  const config = useStore((s) => s.config)
  useEffect(() => {
    if (!config) return
    let active = true
    const entry = reads.get(config) ?? { last: -Infinity }
    reads.set(config, entry)
    const read = async () => {
      if (document.hidden) return
      if (Date.now() - entry.last >= 30_000) {
        entry.last = Date.now()
        entry.pending = (async () => {
          try {
            const res = await fetch(`${config.apiUrl}/api/linear`, { headers: { Authorization: `Bearer ${config.token}` } })
            return res.ok ? await res.json() as Sprint : null
          } catch { return null }
        })()
      }
      const data = await entry.pending
      if (active && data) useSprint.setState({ data })
    }
    void read()
    const timer = setInterval(() => void read(), 30_000)
    document.addEventListener('visibilitychange', read)
    refresh.addEventListener('refresh', read)
    return () => { active = false; clearInterval(timer); document.removeEventListener('visibilitychange', read); refresh.removeEventListener('refresh', read) }
  }, [config])
}

export function mismatch(issue: LinearIssue, progress?: { done: number; total: number }): string | undefined {
  if (!progress?.total || issue.status_type === 'canceled') return
  const open = progress.total - progress.done
  if (issue.status_type === 'completed' && open > 0) return `Ticket Done, ${open} open steps`
  if (issue.status_type !== 'completed' && open === 0) return `Steps done, ticket still ${issue.status_name}`
}
export function statusTone(issue: LinearIssue): string {
  if (issue.status_name.toLowerCase() === 'blocked') return 'err'
  if (/review|approval/i.test(issue.status_name)) return 'warn'
  return issue.status_type === 'completed' ? 'ok' : issue.status_type === 'started' ? 'run' : 'muted'
}

/** working on the workstream counts its Manager and Operators; on a track, that track's live Operators. */
export interface ProgressWorkstream { key: string; title: string; tickets?: string[]; working?: number; tracks?: { title: string; tickets?: string[]; working?: number }[] }
/** workstreams lists every workstream carrying the ticket (in sidebar order); workstream is the first. worked: an agent is on whichever carries it. */
export interface ProgressRow { issue: LinearIssue; workstream?: { key: string; title: string }; workstreams: { key: string; title: string }[]; worked: boolean }
const done = (i: LinearIssue) => i.status_type === 'completed'
/**
 * touched: the ticket left the backlog. In Progress, In Review, Approval, Blocked and Done
 * count; Todo, Backlog, Triage and Canceled do not. Review/Approval/Blocked count by name
 * whatever type the team gave them; the rest go by Linear's status type.
 */
const TOUCHED_NAMES = /^(in progress|in review|approval|blocked|done)$/i
export const isTouched = (i: LinearIssue) => TOUCHED_NAMES.test(i.status_name.trim()) || i.status_type === 'started' || done(i)
export const touched = (issues: LinearIssue[]) => issues.filter(isTouched).length

const DATE_ONLY = /^(\d{4})-(\d{2})-(\d{2})$/
const DAY = 86_400_000

/** zoneDay is the calendar day an instant falls on in a zone, as UTC midnight of that day ('' or an unknown zone = the browser's). */
function zoneDay(ms: number, timeZone?: string): number {
  const fmt = (tz?: string) => new Intl.DateTimeFormat('en-US', { year: 'numeric', month: 'numeric', day: 'numeric', timeZone: tz || undefined }).formatToParts(ms)
  let parts: Intl.DateTimeFormatPart[]
  try { parts = fmt(timeZone) } catch { parts = fmt() }
  const n = (type: string) => Number(parts.find((p) => p.type === type)?.value)
  return Date.UTC(n('year'), n('month') - 1, n('day'))
}

/**
 * cycleDay is a cycle boundary as a calendar day (UTC midnight of it). The end is exclusive, the
 * start of the next day, so lastDay steps back one: a calendar day for a YYYY-MM-DD value, kept as
 * given in every zone, and one millisecond for an instant, which is read in the user's zone.
 */
function cycleDay(value: string | undefined, timeZone: string | undefined, lastDay: boolean): number | null {
  const date = value ? DATE_ONLY.exec(value) : null
  if (date) return Date.UTC(+date[1], +date[2] - 1, +date[3]) - (lastDay ? DAY : 0)
  const ms = value ? Date.parse(value) : NaN
  return isNaN(ms) ? null : zoneDay(lastDay ? ms - 1 : ms, timeZone)
}

/** cycleLastDay formats a cycle's end as its last day, or with lastDay false a boundary as it is (the start). */
export function cycleLastDay(value: string | undefined, timeZone?: string, lastDay = true): string {
  const day = cycleDay(value, timeZone, lastDay)
  return day === null ? '' : new Date(day).toLocaleDateString('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' })
}

/** cycleDaysLeft counts calendar days from today to the cycle's last day: 0 on the last day, negative once it ended. */
export function cycleDaysLeft(end: string | undefined, timeZone: string | undefined, now: number): number | null {
  const last = cycleDay(end, timeZone, true)
  return last === null ? null : Math.round((last - zoneDay(now, timeZone)) / DAY)
}

export function daysLeftText(n: number): string {
  if (n < 0) return 'ended'
  if (n === 0) return 'ends today'
  return `${n} day${n === 1 ? '' : 's'} left`
}

/**
 * progressRows joins each ticket of the cycle to the workstream (or track's workstream)
 * whose title names it or that carries a ticket:<ID> label, every one when several do, judged by
 * whichever carries it (a track by its own Operators). Tickets with no workstream that are not Done come first.
 */
export function progressRows(issues: LinearIssue[], workstreams: ProgressWorkstream[], ticketsOf: (title: string) => string[]): ProgressRow[] {
  const owners = new Map<string, { key: string; title: string }[]>()
  const worked = new Set<string>()
  const claim = (w: ProgressWorkstream, ids: string[], working?: number) => {
    for (const t of ids) {
      const list = owners.get(t) ?? []
      if (!list.some((o) => o.key === w.key)) list.push({ key: w.key, title: w.title })
      owners.set(t, list)
      if ((working ?? 0) > 0) worked.add(t)
    }
  }
  for (const w of workstreams) {
    claim(w, [...ticketsOf(w.title), ...(w.tickets ?? [])], w.working)
    for (const x of w.tracks ?? []) claim(w, [...ticketsOf(x.title), ...(x.tickets ?? [])], x.working)
  }
  const rows = issues.map((issue) => { const id = issue.id.toUpperCase(); const ws = owners.get(id) ?? []; return { issue, workstream: ws[0], workstreams: ws, worked: worked.has(id) } })
  const rank = (r: ProgressRow) => (!r.workstream && !done(r.issue) ? 0 : 1)
  return rows.map((r, i) => ({ r, i })).sort((a, b) => rank(a.r) - rank(b.r) || a.i - b.i).map((x) => x.r)
}
