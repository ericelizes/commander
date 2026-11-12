import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SnapStep, SnapWorkstream } from '../../shared/snapshot'
import { MyProgress, ProgressPage, progressWorkstreams, sprintStats } from './MyProgress'
import { progressRows, touched, cycleLastDay, cycleDaysLeft, daysLeftText, useSprint, type LinearIssue } from './sprint'
import { splitTickets, ticketIds } from './Ticket'
import { useIntegrations } from './settings'
const mk = (id: string, t: string, n: string): LinearIssue => ({ id, title: id + ' title', status_name: n, status_type: t, url: '' })
const issues = [mk('TEAM-1', 'completed', 'Done'), mk('TEAM-2', 'started', 'In Progress'), mk('TEAM-3', 'unstarted', 'Todo'), mk('TEAM-4', 'completed', 'Done')]
const ws = [{ key: 'a', title: 'Alpha (TEAM-1)' }, { key: 'b', title: 'Beta', tracks: [{ title: 'Track (TEAM-2)' }] }]
afterEach(() => { cleanup(); useSprint.setState({ data: null }); useIntegrations.setState({ timezone: '' }) })
const tix = (t: string) => splitTickets(t).tickets
const view = (onOpen = vi.fn()) => render(<MyProgress workstreams={ws} onOpen={onOpen} />)

it('joins tickets to workstreams and sorts unowned open tickets first', () => {
  const rows = progressRows(issues, ws, tix)
  expect(rows.map((r) => r.issue.id)).toEqual(['TEAM-3', 'TEAM-1', 'TEAM-2', 'TEAM-4'])
  expect(rows[2].workstream?.key).toBe('b')
  expect(touched(issues)).toBe(3)
})
it('summarises to do, in progress and done; a click opens the page, which breaks them down', () => {
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues, cycle: { name: 'Cycle 12', start: '2026-10-28T04:00:00Z', end: '2026-11-10T12:00:00Z' } } })
  const open = vi.fn()
  view(open)
  expect(screen.getByText('to do').previousSibling?.textContent).toBe('2')
  expect(screen.getByText('in progress').previousSibling?.textContent).toBe('0')
  expect(screen.getByText('done').previousSibling?.textContent).toBe('2')
  fireEvent.click(screen.getByRole('button', { name: /^Progress/ }))
  expect(open).toHaveBeenCalledOnce()
  cleanup()
  open.mockClear()
  render(<ProgressPage workstreams={ws} onOpen={open} />)
  expect(screen.getByRole('heading', { name: 'Cycle 12' })).toBeTruthy()
  expect(screen.getByRole('tab', { name: /Wrong status/ })).toBeTruthy()
  fireEvent.click(screen.getByRole('tab', { name: /Finished/ }))
  expect(screen.getByText('TEAM-1 title')).toBeTruthy()
  fireEvent.click(screen.getByText('Alpha'))
  expect(open).toHaveBeenCalledWith('a')
})
it('hides everything when off', () => {
  useSprint.setState({ data: { state: 'off', key: 'not set', issues: [] } })
  expect(view().container.innerHTML).toBe('')
})
it('explains no_key, bad_key and dims the last list when unreachable', () => {
  useSprint.setState({ data: { state: 'no_key', key: 'not set', issues: [] } })
  view()
  expect(screen.getByRole("button", { name: "Connect Linear" })).toBeTruthy()
  cleanup()
  useSprint.setState({ data: { state: 'bad_key', key: 'saved', issues: [] } })
  view()
  expect(screen.getByText(/rejected the saved key/)).toBeTruthy()
  cleanup()
  useSprint.setState({ data: { state: 'unreachable', key: 'saved', issues } })
  const { container } = view()
  expect(container.querySelector('.sp-card.dimmed')).toBeTruthy()
})

it('counts In Review, Approval and Blocked as touched, Todo/Backlog/Triage/Canceled not', () => {
  const all = [mk('A-1', 'started', 'In Review'), mk('A-2', 'started', 'Approval'), mk('A-3', 'started', 'Blocked'), mk('A-4', 'unstarted', 'Todo'), mk('A-5', 'backlog', 'Backlog'), mk('A-6', 'triage', 'Triage'), mk('A-7', 'canceled', 'Canceled')]
  expect(touched(all)).toBe(3)
})
it('shows the last day of the cycle in the user zone', () => {
  expect(cycleLastDay('2026-11-11T00:00:00Z', 'America/New_York')).toBe('Nov 10')
  expect(cycleLastDay('2026-11-11T00:00:00Z', 'Not/AZone')).toMatch(/^Nov (10|11)$/)
  useIntegrations.setState({ timezone: 'America/New_York' })
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues, cycle: { name: 'Cycle 12', start: '2026-10-28T04:00:00Z', end: '2026-11-11T00:00:00Z' } } })
  render(<ProgressPage workstreams={ws} onOpen={vi.fn()} />)
  expect(screen.getByText(/Oct 28 to Nov 10/)).toBeTruthy()
})
it('joins a workstream by its ticket: label', () => {
  const labelled = [{ key: 'p:w', title: 'Plain title', tickets: ['TEAM-4321'] }]
  const one = [mk('TEAM-4321', 'started', 'In Progress')]
  expect(progressRows(one, labelled, tix)[0].workstream?.key).toBe('p:w')
  const track = [{ key: 'p:x', title: 'Other', tracks: [{ title: 'T', tickets: ['TEAM-4321'] }] }]
  expect(progressRows(one, track, tix)[0].workstream?.key).toBe('p:x')
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues: one, cycle: { name: 'C' } } })
  render(<ProgressPage workstreams={labelled} onOpen={vi.fn()} />)
  expect(screen.getByText('Plain title')).toBeTruthy()
})
it('keeps every workstream for a ticket carried by several, worked when any of them is', () => {
  const two = [{ key: 'x', title: 'X (TEAM-9)' }, { key: 'y', title: 'Y', tickets: ['TEAM-9'], working: 1 }]
  const one = [mk('TEAM-9', 'unstarted', 'Todo')]
  expect(progressRows(one, two, tix)[0].workstreams.map((w) => w.key)).toEqual(['x', 'y'])
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues: one, cycle: { name: 'C' } } })
  render(<MyProgress workstreams={two} onOpen={vi.fn()} />)
  expect(screen.getByText('in progress').previousSibling?.textContent).toBe('1')
  cleanup()
  const onOpen = vi.fn()
  render(<ProgressPage workstreams={two} onOpen={onOpen} />)
  fireEvent.click(screen.getByRole('tab', { name: /In progress/ }))
  expect(screen.getByRole('button', { name: 'X' })).toBeTruthy()
  fireEvent.click(screen.getByRole('button', { name: 'Y' }))
  expect(onOpen).toHaveBeenCalledWith('y')
})

const opened = (ws: Parameters<typeof sprintStats>[1], list: LinearIssue[]) => sprintStats(list, ws, null, 0)
it('says no active cycle with a saved key, and offers no key button', () => {
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues: [], cycle: null } })
  view()
  expect(screen.getByText('No active cycle')).toBeTruthy()
  expect(screen.queryByRole('button')).toBeNull()
  expect(document.body.textContent).not.toMatch(/Connect Linear|Replace key/)
})
it('only no_key connects and only bad_key replaces', () => {
  useSprint.setState({ data: { state: 'no_key', key: 'not set', issues: [] } })
  view()
  expect(screen.queryByRole('button', { name: 'Replace key' })).toBeNull()
  cleanup()
  useSprint.setState({ data: { state: 'bad_key', key: 'saved', issues: [] } })
  view()
  expect(screen.getByRole('button', { name: 'Replace key' })).toBeTruthy()
  expect(screen.queryByRole('button', { name: 'Connect Linear' })).toBeNull()
})
it('says the numbers are old when Linear is unreachable', () => {
  const read = new Date(Date.now() - 5 * 60_000).toISOString()
  useSprint.setState({ data: { state: 'unreachable', key: 'saved', issues, cycle: { name: 'C' }, fetched_at: read } })
  view()
  expect(screen.getByRole('button', { name: /^Progress/ }).textContent).toMatch(/Linear unreachable, last read 5m ago/)
  cleanup()
  useSprint.setState({ data: { state: 'unreachable', key: 'saved', issues: [], cycle: null } })
  view()
  expect(screen.getByText('Linear unreachable')).toBeTruthy()
  expect(screen.queryByRole('button')).toBeNull()
  expect(screen.queryByText('to do')).toBeNull()
})
it('never shows a key value, only that one is saved', () => {
  useSprint.setState({ data: { state: 'ok', key: 'saved', issues, cycle: { name: 'C' } } })
  view()
  fireEvent.click(screen.getByRole('button', { name: /^Progress/ }))
  expect(document.body.textContent).not.toMatch(/lin_api|private-test-value|saved/)
})
describe('days left', () => {
  afterEach(() => vi.useRealTimers())
  const end = '2026-11-11T00:00:00Z' // the last day in New York is Nov 10
  const at = (iso: string) => cycleDaysLeft(end, 'America/New_York', Date.parse(iso))
  it('counts calendar days to the last day in the user zone', () => {
    expect(at('2026-11-09T17:00:00Z')).toBe(1) // Nov 9, noon
    expect(at('2026-11-10T01:00:00Z')).toBe(1) // Nov 9, 8pm: the same day still
    expect(at('2026-11-10T17:00:00Z')).toBe(0) // Nov 10, noon
    expect(at('2026-11-11T01:00:00Z')).toBe(0) // Nov 10, 8pm
    expect(at('2026-11-11T17:00:00Z')).toBe(-1) // Nov 11
    expect([daysLeftText(2), daysLeftText(1), daysLeftText(0), daysLeftText(-3)]).toEqual(['2 days left', '1 day left', 'ends today', 'ended'])
  })
  it('moves on the clock with no new data, in the card and the page', async () => {
    vi.useFakeTimers({ now: Date.parse('2026-11-11T04:30:00Z') }) // Nov 10, 11:30pm in New York
    useIntegrations.setState({ timezone: 'America/New_York' })
    useSprint.setState({ data: { state: 'ok', key: 'saved', issues, cycle: { name: 'C', start: '2026-10-28T04:00:00Z', end } } })
    view()
    expect(screen.getByText('ends today')).toBeTruthy()
    act(() => { vi.advanceTimersByTime(31 * 60_000) })
    expect(screen.getByText('ended')).toBeTruthy()
    cleanup()
    render(<ProgressPage workstreams={ws} onOpen={vi.fn()} />)
    expect(screen.getByText(/Oct 28 to Nov 10 · ended/)).toBeTruthy()
  })
})
it('keeps date-only cycle values as calendar dates in every zone', () => {
  for (const zone of ['America/New_York', 'Asia/Tokyo', 'UTC']) {
    expect(cycleLastDay('2026-10-28', zone, false)).toBe('Oct 28')
    expect(cycleLastDay('2026-11-11', zone)).toBe('Nov 10')
  }
  expect(cycleDaysLeft('2026-11-11', 'Asia/Tokyo', Date.parse('2026-11-10T03:00:00+09:00'))).toBe(0)
  expect(cycleLastDay('nonsense', 'UTC')).toBe('')
})
it('joins any ticket id spelling a title can hold, and never a word that is no ticket', () => {
  expect(ticketIds('op_TEAM-12 team-4321 PLATFORM-12, TEAM-12')).toEqual(['TEAM-12', 'TEAM-4321', 'PLATFORM-12'])
  expect(ticketIds('x-TEAM-1 TEAM-1a TOOLONGAKEY-1')).toEqual([])
  const rows = progressRows([mk('TEAM-4321', 'unstarted', 'Todo')], [{ key: 'a', title: 'lowercase team-4321 in utf-8' }], ticketIds)
  expect(rows[0].workstream?.key).toBe('a')
  expect(progressRows([mk('TEAM-1', 'unstarted', 'Todo')], [{ key: 'a', title: 'utf-8' }], ticketIds)[0].workstream).toBeUndefined()
})
it('gives every ticket of a workstream that carries several, with punctuation around the ids', () => {
  const rows = progressRows([mk('TEAM-1', 'started', 'In Progress'), mk('TEAM-2', 'started', 'In Progress')], [{ key: 'a', title: 'Jobs: TEAM-1/TEAM-2.' }], ticketIds)
  expect(rows.map((r) => r.workstream?.key)).toEqual(['a', 'a'])
})
it('judges a track ticket by its own track, not a sibling or the Manager', () => {
  const list = [mk('TEAM-1', 'unstarted', 'Todo'), mk('TEAM-2', 'started', 'In Progress')]
  const ws = [{ key: 'a', title: 'A', working: 2, tracks: [{ title: 'Idle (TEAM-1)', working: 0 }, { title: 'Busy (TEAM-2)', working: 1 }] }]
  const s = opened(ws, list)
  expect(s.started.map((r) => r.issue.id)).toEqual(['TEAM-2'])
  expect(s.todo.map((r) => r.issue.id)).toEqual(['TEAM-1'])
  expect(s.wrong).toEqual([])
})
it('flags only In Progress with no agent, and an agent on a ticket not started', () => {
  const list = [mk('A-1', 'started', 'In Review'), mk('A-2', 'started', 'Approval'), mk('A-3', 'started', 'Blocked'), mk('A-4', 'started', 'In Progress'), mk('A-5', 'unstarted', 'Todo'), mk('A-6', 'started', 'In Review')]
  const s = opened([{ key: 'a', title: 'x', working: 0, tickets: ['A-1', 'A-2', 'A-3', 'A-4', 'A-5'] }, { key: 'b', title: 'y', working: 1, tickets: ['A-6'] }], list)
  expect(s.wrong.map((r) => r.issue.id)).toEqual(['A-4'])
  const busy = opened([{ key: 'a', title: 'x', working: 1, tickets: ['A-5'] }], list)
  expect(busy.wrong.map((r) => r.issue.id)).toEqual(['A-4', 'A-5'])
})
describe('progressWorkstreams', () => {
  const step = (id: string, over: Partial<SnapStep>) => ({ bead_id: id, title: id, status: 'open', ...over }) as SnapStep
  const snap = { project: 'p', bead_id: 'w', title: 'W', tickets: ['TEAM-9'], steps: [
    step('w.1', { title: 'Busy (TEAM-1)', track: true, children: [step('w.1.1', { owner: 'operator-x', owner_state: 'working' })] }),
    step('w.2', { title: 'Idle', tickets: ['TEAM-2'], track: true, children: [step('w.2.1', { owner: 'operator-y', owner_state: 'died' })] }),
    step('w.3', { title: 'Closed (TEAM-3)', track: true, status: 'closed' }),
    step('w.4', { title: 'Parked (TEAM-4)', track: true, status: 'deferred' }),
    step('w.5', { title: 'Dropped (TEAM-5)', track: true, superseded: true }),
    step('w.6', { title: 'A plain step (TEAM-6)' }),
  ] } as unknown as SnapWorkstream
  it('keeps open tracks only, each with its own live Operators', () => {
    const [w] = progressWorkstreams([snap], [{ key: 'p:w', working: 2 }])
    expect(w).toMatchObject({ key: 'p:w', title: 'W', tickets: ['TEAM-9'], working: 2 })
    expect(w.tracks).toEqual([{ title: 'Busy (TEAM-1)', tickets: undefined, working: 1 }, { title: 'Idle', tickets: ['TEAM-2'], working: 0 }])
  })
  it('lets a closed, parked or dropped track claim nothing', () => {
    const list = [mk('TEAM-1', 'unstarted', 'Todo'), mk('TEAM-2', 'unstarted', 'Todo'), mk('TEAM-3', 'unstarted', 'Todo'), mk('TEAM-4', 'unstarted', 'Todo'), mk('TEAM-5', 'unstarted', 'Todo'), mk('TEAM-9', 'unstarted', 'Todo')]
    const s = opened(progressWorkstreams([snap], [{ key: 'p:w', working: 2 }]), list)
    expect(s.unassigned.map((r) => r.issue.id)).toEqual(['TEAM-3', 'TEAM-4', 'TEAM-5'])
    expect(s.started.map((r) => r.issue.id)).toEqual(['TEAM-1', 'TEAM-9'])
    expect(s.wrong.map((r) => r.issue.id)).toEqual(['TEAM-1', 'TEAM-9'])
  })
})

it('shows different team cycles and checks all teams during partial availability', () => {
 const merged = [mk('FOR-1', 'started', 'In Progress'), mk('ENGCHA-5197', 'completed', 'Done'), mk('ENGINT-2', 'unstarted', 'Todo')]
 useSprint.setState({data:{state:'partial',key:'saved',issues:merged,teams:[
 {team:'FOR',state:'unreachable',cycle:{name:'Cycle A',start:'2026-11-01',end:'2026-11-15'},fetched_at:'2026-11-12T10:00:00Z'},
 {team:'ENGCHA',state:'ok',cycle:{name:'CareHub',start:'2026-11-02',end:'2026-11-16'}},
 {team:'ENGINT',state:'ok',cycle:{name:'Internal',start:'2026-11-03',end:'2026-11-17'}}]}})
 render(<ProgressPage workstreams={[]} onOpen={vi.fn()} />)
 expect(screen.getByText(/FOR: Cycle A/)).toHaveTextContent('Linear unreachable')
 expect(screen.getByText(/ENGCHA: CareHub/)).toHaveTextContent('Nov 2 to Nov 15')
 expect(screen.getByRole('tab',{name:/Wrong status/})).toHaveTextContent('1')
 fireEvent.click(screen.getByRole('tab',{name:/Finished/}))
 expect(screen.getByText('ENGCHA-5197 title')).toBeTruthy()
 fireEvent.click(screen.getByRole('tab',{name:/To do/}))
 expect(screen.getByText('ENGINT-2 title')).toBeTruthy()
})

it('judges added tickets against their own team cycle start', () => {
 const rows = [{...mk('FOR-1','unstarted','Todo'),team:'FOR',added_at:'2026-11-03'}, {...mk('ENGCHA-1','unstarted','Todo'),team:'ENGCHA',added_at:'2026-11-03'}]
 const stats = sprintStats(rows, [], null, Date.now(), 'UTC', [
 {team:'FOR',state:'ok',cycle:{name:'A',start:'2026-11-01'}},
 {team:'ENGCHA',state:'ok',cycle:{name:'B',start:'2026-11-03'}}])
 expect(stats.added.map((r)=>r.issue.id)).toEqual(['FOR-1'])
})
