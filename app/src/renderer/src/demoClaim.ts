// Demo only (?fixture=chat&claim=1): workstream 1 as example-app t01, to see
// the claim and the next gate without cmdrd. An Operator claimed 17.2 and its
// process is gone (it never vanishes: it reads "died"); the PR approval gate
// is open with a done child and a review wait, so it, not the merge, is next.

import type { Snapshot, SnapStep } from '../../shared/snapshot'

const step = (id: string, title: string, state: string, extra: Partial<SnapStep> = {}): SnapStep => ({
  bead_id: `example-app-t01.${id}`,
  title,
  state,
  status: state === 'done' ? 'closed' : 'open',
  ...extra,
})

export function withClaim(snap: Snapshot): Snapshot {
  const s = JSON.parse(JSON.stringify(snap)) as Snapshot
  const ws = s.workstreams[0]
  ws.steps = [
    step('2', 'Alert rules and dashboard', 'done'),
    step('17', 'PR approval', 'not-started', {
      gate: true,
      approvers: ['reviewer-a', 'reviewer-b'],
      blocked_by: ['example-app-t01.2'],
      waiting: 'review (reviewer-a, reviewer-b)',
      children: [step('17.1', 'Make the metrics PRs one strict stack', 'done')],
    }),
    step('17.2', 'Fix the stack after review', 'in-progress', { owner: 'operator-op-fix-stack-example-app-t01-17-2', owner_state: 'died' }),
    step('18', 'Merge the stack', 'not-started', { gate: true, approvers: ['user'], blocked_by: ['example-app-t01.17'] }),
  ]
  return s
}
