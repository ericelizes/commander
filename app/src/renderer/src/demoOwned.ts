// Demo only (?fixture=chat&owned=1): workstream 1 reshaped as an owned job,
// "keep the job worker stack mergeable", so the design can be seen before
// cmdrd and the Managers produce it.

import type { Snapshot, SnapStep } from '../../shared/snapshot'

const stage = (n: number, title: string, state: string, note?: string): SnapStep => ({ bead_id: `example-app-t03.9.${n}`, title, state, status: state === 'done' ? 'closed' : 'open', note })

export function withOwnedJob(snap: Snapshot): Snapshot {
  const s = JSON.parse(JSON.stringify(snap)) as Snapshot
  const ws = s.workstreams[0]
  const find = (title: string) => ws.steps.find((x) => x.title.startsWith(title))!
  const job: SnapStep = {
    bead_id: 'example-app-t03.9',
    title: 'Keep the job worker stack mergeable and top quality (TEAM-101)',
    state: 'in-progress',
    status: 'in_progress',
    owner: ws.manager_role,
    until: 'merged',
    review: '101',
    children: [
      stage(1, 'Plan', 'done'),
      stage(2, 'Implement', 'done'),
      stage(3, 'Self-review', 'done', '2 passes, findings fixed'),
      stage(4, 'CI green', 'done', '25 of 25 checks'),
      stage(5, 'Review comments', 'in-progress', '3 open'),
      stage(6, 'Mergeable', 'not-started'),
    ],
  }
  // &state=working|ready|needs|stale|merged picks which moment of the job to show.
  const state = new URLSearchParams(location.search).get('state') ?? 'working'
  const kids = job.children!
  let since = 240
  if (state === 'ready' || state === 'merged') kids.forEach((k) => ((k.state = 'done'), (k.status = 'closed')))
  if (state === 'merged') ((job.state = 'done'), (job.status = 'closed'))
  if (state === 'needs') kids[4] = { ...kids[4], state: 'needs-user', note: 'reviewer-a asked for a schema change the spec rules out: which wins?' }
  if (state === 'stale') since = 16 * 3600
  // The spike that wrote the tech spec, finished and approved: the job builds on it.
  const spike: SnapStep = { bead_id: 'example-app-t03.8', title: 'Tech spec spike · approved', state: 'done', status: 'closed' }
  job.blocked_by = [spike.bead_id]
  const goLive = { ...find('Go live'), blocked_by: [job.bead_id] }
  const pilot = { ...find('First pilot'), blocked_by: [goLive.bead_id] }
  ws.steps = [spike, job, goLive, pilot]
  s.agents[ws.manager_role] = { state: state === 'working' ? 'working' : 'idle', since: Math.floor(Date.now() / 1000) - since }
  return s
}
