// FocusHeader names the workstream in focus, in the top bar over the
// focus pane.

import { splitTickets } from '../../../features/linear/Ticket'
import type { BoardProgress, LineKind } from '../../../shared/snapshot'

export interface FocusTarget {
  n?: number
  title: string
  letter?: string
  track?: string
  kind?: LineKind
  text?: string
  progress?: BoardProgress
}

export function FocusHeader({ target }: { target: FocusTarget }) {
  const { n, title, letter, track } = target
  const name = `Workstream ${n ?? 0}${letter ?? ''}`
  return (
    <h2 className="app-top-title" title={track ? `${n ? `${n} ` : ''}${title}` : undefined}>
      {name}: {splitTickets(track ?? title).text}
    </h2>
  )
}
