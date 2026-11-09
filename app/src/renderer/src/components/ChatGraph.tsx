// A graph an agent draws in chat (a ```cmdr-graph block), on the same canvas
// as the Graph tab. The agent writes the beads it wants to show, nested like
// beads are, and may mark each as a proposed change:
//
//   { "title": "Background jobs stack", "workstream": "example-app-t03",
//     "nodes": [ { "id": "a", "title": "Explore the sample design", "state": "done" },
//                { "id": "b", "title": "Go live", "blocked_by": ["a"], "change": "add",
//                  "children": [ ... ] } ] }
//
// "workstream" (optional) names a real workstream: its Manager tags agents and
// "Show in graph" opens it. A workflow marks its approval points with
// "gate": true (and "approvers": ["user"] or reviewer logins) and its tracks
// with "track": true; with any gate it is drawn as lanes, gate, the work
// between, gate, like the Graph tab, instead of as boxes.

import { useLayoutEffect, useMemo, useRef, useState } from 'react'
import type { SnapStep, SnapWorkstream, Snapshot } from '../../../shared/snapshot'
import { buildCanvas } from '../graph'
import { useStore } from '../store'
import { Workflow } from './FlowGraph'
import { CanvasWorld } from './TaskGraph'

interface SpecNode {
  id?: unknown
  title?: unknown
  state?: unknown
  owner?: unknown
  until?: unknown
  pr?: unknown
  blocked_by?: unknown
  change?: unknown
  gate?: unknown
  approvers?: unknown
  means?: unknown
  track?: unknown
  children?: unknown
}

const STATES = new Set(['not-started', 'in-progress', 'review', 'needs-user', 'blocked', 'done'])
const CHANGES = new Set(['add', 'remove', 'edit'])

const str = (v: unknown) => (typeof v === 'string' && v ? v : undefined)

function toSteps(nodes: unknown, path: string): SnapStep[] {
  if (!Array.isArray(nodes)) return []
  return nodes.map((raw, i): SnapStep => {
    const n = (raw ?? {}) as SpecNode
    const id = str(n.id) ?? `${path}${i + 1}`
    const state = str(n.state) ?? 'not-started'
    return {
      bead_id: id,
      title: str(n.title) ?? 'Untitled step',
      state: STATES.has(state) ? state : 'not-started',
      status: state === 'done' ? 'closed' : 'open',
      owner: str(n.owner),
      until: str(n.until),
      review: n.pr != null ? String(n.pr) : undefined,
      blocked_by: Array.isArray(n.blocked_by) ? n.blocked_by.filter((b): b is string => typeof b === 'string') : undefined,
      change: CHANGES.has(String(n.change)) ? (n.change as SnapStep['change']) : undefined,
      gate: n.gate === true || undefined,
      approvers: Array.isArray(n.approvers) ? n.approvers.filter((a): a is string => typeof a === 'string') : n.gate === true ? ['user'] : undefined,
      note: str(n.means),
      track: n.track === true || undefined,
      children: toSteps(n.children, `${id}.`),
    }
  })
}

export function ChatGraph({ spec }: { spec: unknown }) {
  const snapshot = useStore((s) => s.snapshot)
  const openLink = useStore((s) => s.openLink)
  const s = (spec ?? {}) as { title?: unknown; workstream?: unknown; nodes?: unknown }
  const real = snapshot?.workstreams.find((w) => w.bead_id === s.workstream)

  const ws = useMemo((): SnapWorkstream => ({
      n: 0,
      project: real?.project ?? 'chat',
      project_id: real?.project_id ?? 0,
      bead_id: real?.bead_id ?? 'graph',
      title: str(s.title) ?? real?.title ?? 'Graph',
      state: 'not-started',
      status: 'open',
      manager_role: real?.manager_role ?? '',
      done: 0,
      total: 0,
      steps: toSteps(s.nodes, ''),
  }), [spec, real]) // eslint-disable-line react-hooks/exhaustive-deps
  const canvas = useMemo(() => buildCanvas({ workstreams: [ws] } as unknown as Snapshot, `${ws.project}:${ws.bead_id}`), [ws])
  const isWorkflow = useMemo(() => hasGate(ws.steps), [ws])

  // Fit the chat column: shrink a wide graph, never enlarge a small one.
  const host = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(0)
  useLayoutEffect(() => {
    const el = host.current
    if (!el) return
    const measure = () => setWidth(el.clientWidth)
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const k = width ? Math.min(1, width / canvas.width) : 1
  const proposed = canvas.cards.some((c) => c.change) || canvas.boxes.some((b) => b.change)

  if (isWorkflow) {
    return (
      <figure className="cc-graph cc-graph-workflow">
        <figcaption className="cc-graph-head">
          <span>{str(s.title) ?? 'Workflow'}</span>
          {real && (
            <button type="button" className="cc-bead-link" onClick={() => openLink(real.bead_id, 'workflow')}>
              Open workflow
            </button>
          )}
        </figcaption>
        <Workflow ws={ws} />
      </figure>
    )
  }
  return (
    <figure className="cc-graph">
      <figcaption className="cc-graph-head">
        <span>{proposed ? 'Proposed changes' : 'Graph'}</span>
        {real && (
          <button type="button" className="cc-bead-link" onClick={() => openLink(real.bead_id, 'workflow')}>
            Open workflow
          </button>
        )}
      </figcaption>
      <div ref={host} className="cc-graph-body" style={{ height: canvas.height * k }}>
        <div className="canvas-world" style={{ transform: `scale(${k})`, width: canvas.width, height: canvas.height }}>
          <CanvasWorld canvas={canvas} />
        </div>
      </div>
    </figure>
  )
}

const hasGate = (steps: SnapStep[]): boolean => steps.some((x) => x.gate || hasGate(x.children ?? []))
