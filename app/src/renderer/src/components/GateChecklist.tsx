import type { SnapGate } from '../../../shared/snapshot'
import type { GateTone } from '../gateTone'

const MARK = { passed: '✓', failed: '✗', open: '○' } as const

// failedDetail is what a failed gate says: that it failed, and its evidence.
export const failedDetail = (g: { evidence?: string }) => (g.evidence ? `failed, ${g.evidence}` : 'failed')

// gateTitle is the hover text: the evidence for a passed gate, else who checks it.
export function gateTitle(g: SnapGate): string {
  if (g.status === 'passed') return g.evidence ? `${g.name}: ${g.evidence}` : `${g.name}: passed`
  if (g.status === 'failed') return `${g.name}: ${failedDetail(g)}`
  return `${g.name}: not checked yet`
}

/** GateChecklist is a stage's gates, one line each: mark, name, then its verifier, small. */
export function GateChecklist({ gates, tones }: { gates: SnapGate[]; tones?: GateTone[] }) {
  return (
    <ul className="gate-list" aria-label="Gates">
      {gates.map((g, i) => (
        <li key={g.name} className={`gate-item gate-${g.status}${tones?.[i] ? ` tone-${tones[i]}` : ''}`} title={gateTitle(g)}>
          <span className="gate-check" aria-label={g.status}>
            {MARK[g.status]}
          </span>
          <span className="gate-name">{g.name}</span>
          <span className="gate-verifier">{g.verifier}</span>
        </li>
      ))}
    </ul>
  )
}
