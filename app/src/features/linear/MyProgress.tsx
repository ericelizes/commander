import { ChartNoAxesColumn } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { createPortal } from 'react-dom'
import { SettingsModal } from '../../renderer/src/components/SettingsModal'
import { openInPanel } from '../inspector/connections'
import { fmtAge, openNested, wsKey } from '../../renderer/src/viewmodel'
import type { SnapStep, SnapWorkstream } from '../../shared/snapshot'
import { useSprint, progressRows, cycleLastDay, cycleDaysLeft, daysLeftText, type Cycle, type LinearIssue, type ProgressRow, type ProgressWorkstream, type Sprint, type TeamSprint } from './sprint'
import { useIntegrations } from './settings'
import { splitTickets, ticketIds } from './Ticket'

const DAY = 86_400_000

const isDone = (i: LinearIssue) => i.status_type === 'completed'
const isCanceled = (i: LinearIssue) => i.status_type === 'canceled'
const isStarted = (i: LinearIssue) => i.status_type === 'started'
// In Review, Approval and Blocked are 'started' too, but rightly sit with no agent on them.
const claimsActive = (i: LinearIssue) => /^in progress$/i.test(i.status_name.trim())

const closed = (s: SnapStep) => s.status === 'closed' || s.state === 'done'
const parked = (s: SnapStep) => s.status === 'deferred'

/** liveOperators counts the live Operators on steps and under them (livedata.workingOperators). */
function liveOperators(steps: SnapStep[]): number {
  const ops = new Set<string>()
  const walk = (list: SnapStep[]) => {
    for (const x of list) {
      if (parked(x)) continue
      if (!closed(x) && x.owner?.startsWith('operator-') && (!x.owner_state || x.owner_state === 'working')) ops.add(x.owner)
      walk(x.children ?? [])
    }
  }
  walk(steps)
  return ops.size
}

/**
 * progressWorkstreams is what My Progress joins tickets to, from the snapshot (livedata.WorkstreamTickets):
 * open tracks only, each with its own Operators, single tracks too (the board's rows list only several).
 */
export function progressWorkstreams(workstreams: SnapWorkstream[], rows: { key: string; working: number }[]): ProgressWorkstream[] {
  const working = new Map(rows.map((r) => [r.key, r.working]))
  return workstreams.map((w) => ({
    key: wsKey(w),
    title: w.title,
    tickets: w.tickets,
    working: working.get(wsKey(w)),
    tracks: openNested(w).map((x) => ({ title: x.title, tickets: x.tickets, working: liveOperators([x]) })),
  }))
}

/** unreachableNote says the card's numbers are old, and since when. */
function unreachableNote(data: Sprint, now: number): string {
  const read = Date.parse(data.fetched_at ?? data.stale_since ?? '')
  return isNaN(read) ? 'Linear unreachable' : `Linear unreachable, last read ${fmtAge((now - read) / 1000)} ago`
}

/** StatusRing is Linear's status mark: hollow for to do, half full in progress, full with a check when done. */
export function StatusRing({ issue, size = 14 }: { issue: Pick<LinearIssue, 'status_type'>; size?: number }) {
  const t = issue.status_type
  return (
    <svg className={`sp-ring ${t}`} width={size} height={size} viewBox="0 0 16 16" aria-hidden>
      {t === 'completed' ? (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path d="M4.8 8.2 7 10.3l4.2-4.4" fill="none" stroke="var(--sheet, #fff)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
        </>
      ) : t === 'canceled' ? (
        <>
          <circle cx="8" cy="8" r="7" fill="currentColor" />
          <path d="M5.5 5.5l5 5M10.5 5.5l-5 5" stroke="var(--sheet, #fff)" strokeWidth="1.6" strokeLinecap="round" />
        </>
      ) : (
        <>
          <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" strokeWidth="1.6" strokeDasharray={t === 'backlog' || t === 'triage' ? '2 2' : undefined} />
          {t === 'started' && <path d="M8 4a4 4 0 0 1 0 8z" fill="currentColor" />}
        </>
      )}
    </svg>
  )
}

/** sprintStats is everything the card and the popup say, from the cycle and the workstreams that name its tickets. */
export function sprintStats(issues: LinearIssue[], workstreams: ProgressWorkstream[], cycle: { start?: string; end?: string } | null | undefined, now: number, timeZone?: string, teams?: TeamSprint[]) {
  const rows = progressRows(issues, workstreams, ticketIds)
  const on = (r: ProgressRow) => r.worked
  const live = rows.filter((r) => !isCanceled(r.issue))
  const open = live.filter((r) => !isDone(r.issue))
  const start = cycle?.start ? Date.parse(cycle.start) : NaN
  const end = cycle?.end ? Date.parse(cycle.end) : NaN
  const span = end - start
  const timeGone = span > 0 ? Math.min(1, Math.max(0, (now - start) / span)) : NaN
  const doneShare = live.length ? live.filter((r) => isDone(r.issue)).length / live.length : 0
  return {
    done: live.filter((r) => isDone(r.issue)),
    // What is really happening decides: an agent on it is in progress, none is to do.
    started: open.filter(on),
    todo: open.filter((r) => !on(r)),
    // Linear says otherwise: an agent works a ticket still in To do, or none works one marked In Progress.
    wrong: open.filter((r) => (on(r) ? !isStarted(r.issue) : claimsActive(r.issue))),
    canceled: rows.filter((r) => isCanceled(r.issue)),
    withAgents: open.filter(on),
    idle: open.filter((r) => r.workstream && !on(r)),
    unassigned: open.filter((r) => !r.workstream),
    // Joined after the first day: planning happens on day one, later adds are ad hoc.
    added: rows.filter((r) => {
      const boundary = teams?.find((t) => t.team === r.issue.team)?.cycle?.start
      const began = boundary ? Date.parse(boundary) : start
      return Number.isFinite(began) && r.issue.added_at && Date.parse(r.issue.added_at) > began + DAY
    }),
    doneShare,
    timeGone,
    daysLeft: cycleDaysLeft(cycle?.end, timeZone, now),
    start,
    end,
  }
}

/** useSprintStats reads the sprint and its numbers, for the card and the page. */
function useSprintStats(workstreams: ProgressWorkstream[]) {
  const data = useSprint((s) => s.data)
  const zone = useIntegrations((x) => x.timezone)
  // The clock moves without new data (reads stop while cmdrd is down), so it gets its own tick.
  const [minute, setMinute] = useState(() => Math.floor(Date.now() / 60_000))
  useEffect(() => {
    const timer = setInterval(() => setMinute(Math.floor(Date.now() / 60_000)), 60_000)
    return () => clearInterval(timer)
  }, [])
  const now = minute * 60_000
  const s = useMemo(() => sprintStats(data?.issues ?? [], workstreams, data?.cycle, now, zone, data?.teams), [data, workstreams, now, zone])
  return { data, s }
}

/** hasSprint: there are numbers to show, as opposed to a key to connect or no cycle. */
function hasSprint(data: Sprint) {
  if (data.state === 'off' || data.state === 'no_key' || data.state === 'bad_key') return false
  return !!data.teams?.some((t) => t.cycle) || !!data.cycle || (data.state === 'unreachable' && data.issues.length > 0)
}

/** The sidebar's heading, with the rail's Progress icon so the two read as one thing. */
function ProgressHead({ days }: { days?: string }) {
  return (
    <span className="my-progress-head">
      <ChartNoAxesColumn size={15} aria-hidden />
      <span className="my-progress-name">Progress</span>
      {days && <span className="my-progress-count">{days}</span>}
    </span>
  )
}

/** PaceBar is the sprint on one line: done, then in progress, with a tick at how much time has gone. */
function PaceBar({ s }: { s: ReturnType<typeof sprintStats> }) {
  const total = s.done.length + s.started.length + s.todo.length
  const share = (n: number) => `${total ? (n / total) * 100 : 0}%`
  return (
    <span className="sp-pacebar" aria-hidden>
      <span className="sp-pacebar-done" style={{ width: share(s.done.length) }} />
      <span className="sp-pacebar-started" style={{ width: share(s.started.length) }} />
      {Number.isFinite(s.timeGone) && <span className="sp-pacebar-now" style={{ left: `${s.timeGone * 100}%` }} />}
    </span>
  )
}

/**
 * MyProgress heads the Progress sidebar: done, in progress (an agent is on it)
 * and to do (none is), and how that compares with the time gone. Clicking it
 * opens the Progress page.
 */
export function MyProgress({ workstreams, onOpen }: { workstreams: ProgressWorkstream[]; onOpen: () => void }) {
  const { data, s } = useSprintStats(workstreams)
  const [keyOpen, setKeyOpen] = useState(false)
  if (!data || data.state === 'off') return null
  const keyModal = keyOpen && createPortal(<SettingsModal section="connections" onClose={() => setKeyOpen(false)} />, document.body)
  if (data.state === 'no_key' || data.state === 'bad_key') {
    const bad = data.state === 'bad_key'
    return (
      <section className="my-progress" aria-label="Progress">
        <ProgressHead />
        <div className="my-progress-connect">
          <p className="my-progress-note">{bad ? 'Linear rejected the saved key.' : 'See your Linear tickets here, next to the work.'}</p>
          <button type="button" className={bad ? 'btn' : 'btn btn-primary'} onClick={() => setKeyOpen(true)}>
            {bad ? 'Replace key' : 'Connect Linear'}
          </button>
        </div>
        {keyModal}
      </section>
    )
  }
  const unreachable = data.state === 'unreachable'
  // Nothing to count: between cycles, or the first read failed. Zeros would look like an empty sprint.
  if (!data.cycle && !data.teams?.some((t) => t.cycle) && (!unreachable || data.issues.length === 0)) {
    return (
      <section className="my-progress" aria-label="Progress">
        <ProgressHead />
        <p className="my-progress-note">{unreachable ? unreachableNote(data, Date.now()) : 'No active cycle'}</p>
        <TeamCycles data={data} />
      </section>
    )
  }
  return (
    <>
      <button type="button" className={`my-progress sp-card${unreachable ? ' dimmed' : ''}`} aria-label="Progress, open the page" title={Number.isFinite(s.timeGone) ? `${Math.round(s.doneShare * 100)}% done, ${Math.round(s.timeGone * 100)}% of the time gone` : undefined} onClick={onOpen}>
        <ProgressHead days={s.daysLeft !== null ? daysLeftText(s.daysLeft) : undefined} />
        <PaceBar s={s} />
        <span className="sp-stats">
          <Stat type="unstarted" n={s.todo.length} label="to do" />
          <Stat type="started" n={s.started.length} label="in progress" />
          <Stat type="completed" n={s.done.length} label="done" />
        </span>
        {data.teams && <TeamCycles data={data} />}
        {unreachable && <span className="my-progress-note">{unreachableNote(data, Date.now())}</span>}
      </button>
      {keyModal}
    </>
  )
}

function Stat({ type, n, label }: { type: string; n: number; label: string }) {
  return (
    <span className="sp-stat">
      <StatusRing issue={{ status_type: type }} size={13} />
      <span className="sp-stat-n">{n}</span>
      <span className="sp-stat-label">{label}</span>
    </span>
  )
}

/** ProgressPage is the sprint's breakdown: dates, pace, and the tickets by what is happening to them. */
export function ProgressPage({ workstreams, onOpen }: { workstreams: ProgressWorkstream[]; onOpen: (key: string) => void }) {
  const { data, s } = useSprintStats(workstreams)
  if (!data || data.state === 'off') {
    return (
      <div className="progress-page">
        <div className="progress-col">
          <p className="sp-hint">Progress comes from Linear, which is off.</p>
        </div>
      </div>
    )
  }
  if (!hasSprint(data)) {
    return (
      <div className="progress-page">
        <div className="progress-col">
          <MyProgress workstreams={workstreams} onOpen={() => {}} />
        </div>
      </div>
    )
  }
  return <SprintBreakdown data={data} cycle={data.cycle} s={s} onOpen={onOpen} />
}

function TeamCycles({ data }: { data: Sprint }) {
  const zone = useIntegrations((x) => x.timezone)
  return <span className="sp-team-cycles">{data.teams?.map((t) => <span key={t.team} style={{ display: 'block' }}>
    {t.team}: {t.cycle ? `${t.cycle.name} · ${cycleLastDay(t.cycle.start, zone, false)} to ${cycleLastDay(t.cycle.end, zone)}` : t.state === 'ok' ? 'No active cycle' : ''}
    {t.state !== 'ok' && ` · ${t.state === 'bad_key' ? 'Linear rejected key' : t.state === 'no_key' ? 'No key' : 'Linear unreachable'}${t.fetched_at ? `, last read ${fmtAge((Date.now() - Date.parse(t.fetched_at)) / 1000)} ago` : ''}`}
  </span>)}</span>
}

function SprintBreakdown({ data, cycle, s, onOpen }: { data: Sprint; cycle?: Cycle | null; s: ReturnType<typeof sprintStats>; onOpen: (key: string) => void }) {
  const zone = useIntegrations((x) => x.timezone)
  const name = cycle?.name ?? 'Sprint'
  const pct = (x: number) => `${Math.round(x * 100)}%`
  const tabs = [
    { id: 'started', label: 'In progress', rows: s.started, hint: 'An agent is working on these right now.' },
    { id: 'todo', label: 'To do', rows: s.todo, hint: 'No agent is working on these: no workstream covers them, or theirs is idle. Give them to Commander.' },
    ...(s.wrong.length ? [{ id: 'wrong', label: 'Wrong status', rows: s.wrong, hint: 'Linear disagrees with what is happening. Update these in Linear.' }] : []),
    { id: 'added', label: 'Added', rows: s.added, hint: 'Joined the sprint after its first day.' },
    { id: 'done', label: 'Finished', rows: s.done, hint: '' },
  ]
  const [tab, setTab] = useState(() => (s.wrong.length ? 'wrong' : tabs.find((t) => t.rows.length)?.id ?? 'started'))
  const shown = tabs.find((t) => t.id === tab) ?? tabs[0]
  const list = (rows: ProgressRow[], hint: string) => (
      <section className="sp-section">
        {hint && <p className="sp-hint">{hint}</p>}
        {rows.length === 0 && <p className="sp-hint">Nothing here.</p>}
        {rows.map((row) => { const { issue } = row; return (
          <div key={issue.id} className="sp-item">
            <StatusRing issue={issue} />
            <button type="button" className="sp-item-title" title={`Open ${issue.id} in Linear`} onClick={() => issue.url && openInPanel(issue.url, issue.id)}>
              {issue.title}
            </button>
            <span className="sp-item-meta">
              {issue.id}
              {s.wrong.includes(row) && <span className="sp-item-wrong">Linear says {issue.status_name}</span>}
              {row.workstreams.map((w) => (
                <button key={w.key} type="button" className="sp-item-ws" title={w.title} onClick={() => onOpen(w.key)}>
                  {splitTickets(w.title).text}
                </button>
              ))}
            </span>
          </div>
        )})}
      </section>
    )
  return (
    <div className="progress-page">
      <section className="progress-col" aria-label={name}>
        <h1 className="progress-title">{name}</h1>
        <TeamCycles data={data} />
        <p className="sp-dates">
          {Number.isFinite(s.start) && Number.isFinite(s.end) && `${cycleLastDay(cycle?.start, zone, false)} to ${cycleLastDay(cycle?.end, zone)}`}
          {s.daysLeft !== null && ` · ${daysLeftText(s.daysLeft)}`}
        </p>
        <div className="sp-body">
          <div className="sp-pacebox">
            <div className="sp-bars">
              <span>Done</span>
              <span className="sp-bar"><span style={{ width: pct(s.doneShare) }} /></span>
              <span>{pct(s.doneShare)}</span>
              {Number.isFinite(s.timeGone) && (
                <>
                  <span>Time gone</span>
                  <span className="sp-bar time"><span style={{ width: pct(s.timeGone) }} /></span>
                  <span>{pct(s.timeGone)}</span>
                </>
              )}
            </div>
          </div>
          <div className="sp-tabs" role="tablist">
            {tabs.map((t) => (
              <button key={t.id} type="button" role="tab" aria-selected={t.id === shown.id} className={`sp-tab${t.id === shown.id ? ' on' : ''}`} onClick={() => setTab(t.id)}>
                {t.label}
                <span className="sp-tab-n">{t.rows.length}</span>
              </button>
            ))}
          </div>
          {list(shown.rows, shown.hint)}
        </div>
      </section>
    </div>
  )
}
