// The colour of a gate says where it stands, at a glance:
//   green  approved / passed
//   red    waiting on the user (he has to act), or blocked / failing (auto check failing, review rejected)
//   yellow reached and waiting on others (CI, a reviewer): aware, nothing to do
//   idle   dashed and dimmed: not reached yet (an earlier stage is still open)
import type { SnapGate, SnapStep, SnapWorkstream } from '../../shared/snapshot'

export type GateTone = 'yellow' | 'red' | 'green' | 'idle'

const closed = (s: SnapStep) => s.status === 'closed' || s.state === 'done'

/** stageReached is whether no stage before this one is still open (its own may be). */
export function stageReached(ws: SnapWorkstream, stageId: string): boolean {
  const steps = ws.steps ?? []
  const byId = new Map(steps.map((s) => [s.bead_id, s]))
  const ordered: SnapStep[] = []
  const seen = new Set<string>()
  const visit = (s: SnapStep, path: Set<string>) => {
    if (seen.has(s.bead_id) || path.has(s.bead_id)) return
    path.add(s.bead_id)
    for (const d of s.blocked_by ?? []) {
      const dep = byId.get(d)
      if (dep) visit(dep, path)
    }
    seen.add(s.bead_id)
    ordered.push(s)
  }
  steps.forEach((s) => visit(s, new Set()))
  const at = ordered.findIndex((s) => s.bead_id === stageId)
  if (at < 0) return true
  return ordered.slice(0, at).every(closed)
}

/** checkTone is the colour of a gate check (status + verifier) on a stage. */
export function checkTone(status: SnapGate['status'], verifier: string, reached: boolean): GateTone {
  if (status === 'passed') return 'green'
  if (status === 'failed') return 'red'
  if (!reached) return 'idle'
  return verifier === 'user' ? 'red' : 'yellow'
}

/** stepGateTone is the colour of a gate bead (a step with `gate`), from its state. */
export function stepGateTone(s: Pick<SnapStep, 'state' | 'status' | 'approvers'>): GateTone {
  if (s.status === 'closed' || s.state === 'done') return 'green'
  if (s.status === 'deferred') return 'idle' // parked: out of play, never red
  if (s.state === 'blocked') return 'red'
  if (s.state === 'needs-user') return 'red'
  if (s.state === 'not-started') return 'idle'
  return (s.approvers ?? []).includes('user') ? 'red' : 'yellow'
}

/**
 * stageGateTones is the colour of each of a stage's gate checks, in order.
 * It reads the workstream's gate_nodes and falls back to the stage's own
 * gates[] while the migration to gate beads has not run.
 */
export function stageGateTones(ws: SnapWorkstream, stageId: string, gates: SnapGate[]): GateTone[] {
  const reached = stageReached(ws, stageId)
  const nodes = (ws.gate_nodes ?? []).filter((n) => n.stage === stageId)
  return gates.map((g) => {
    const node = nodes.find((n) => n.name === g.name)
    return checkTone(node?.status ?? g.status, node?.verifier ?? g.verifier, reached)
  })
}
