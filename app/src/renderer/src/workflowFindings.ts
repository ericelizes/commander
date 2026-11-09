import type { DiagramFinding, Snapshot } from '../../shared/snapshot'

export function findingKind(f: DiagramFinding): 'error' | 'blocker' | 'advisory' {
  if (f.advisory) return 'advisory'
  return ['MERGE_REVIEW_INCOMPLETE', 'MERGE_REVIEW_REQUIRED'].includes(f.check) ? 'blocker' : 'error'
}

// A workstream's findings already include its tracks. Counting those again
// would give a different answer from cmdr graph check <workstream>.
export function workflowFindings(snapshot: Snapshot | null) {
  return (snapshot?.display?.workstreams ?? []).flatMap(row =>
    (row.diagram?.findings ?? []).map((f, index) => ({
      key: `${row.key}:${f.check}:${f.bead}:${index}`,
      target: row.key,
      text: `${f.bead}: ${f.why}`,
      kind: findingKind(f),
    })))
}
