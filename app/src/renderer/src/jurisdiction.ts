import type { SnapStep, SnapWorkstream } from '../../shared/snapshot'

// Resolve the selected subtree from the canonical hierarchy, including nested
// workstreams that the sidebar compresses into a parent row.
export function commanderScope(ws: SnapWorkstream, beadId?: string): { beadId: string; owner: string } | null {
  if (!beadId || beadId === ws.bead_id) return ws.planning_commander ? { beadId: ws.bead_id, owner: ws.planning_commander } : null
  const pending: SnapStep[] = [...ws.steps]
  while (pending.length) {
    const step = pending.pop()!
    if (step.bead_id === beadId) return step.planning_commander ? { beadId, owner: step.planning_commander } : null
    if (step.children) pending.push(...step.children)
  }
  return null
}

// Keep the recorded identity distinguishable when several focused conversations exist.
export function commanderName(owner: string): string {
  return owner === 'commander' ? 'Commander' : owner.startsWith('commander-') ? `Commander · ${owner.slice('commander-'.length)}` : owner
}
