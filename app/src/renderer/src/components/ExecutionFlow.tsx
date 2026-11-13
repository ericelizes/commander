// ExecutionFlow draws a pitch's execution workflow as a sequence diagram: one
// vertical line per actor (you, the Manager agent, Operator agents, reviewers,
// automatic checks), each step on its actor's line going down, and an arrow
// wherever the work hands over to another actor. Checkpoints (past tense) say
// what counts as passed. SequenceDiagram is that drawing on its own, for
// steps from anywhere: a drawn graph here, cmdrd's derived diagram in
// DerivedDiagram.

import { useState, type ReactElement } from 'react'
import { ChatGraph } from './ChatGraph'
import { ReviewChecks } from './ReviewChecks'
import type { ReviewBlock } from '../../../shared/snapshot'

interface Node {
  title?: string
  state?: string
  gate?: boolean
  approvers?: string[]
  by?: string
  owner?: string
  criteria?: string
  means?: string
  change?: string // a proposed change to an approved workflow: add, remove or edit
  children?: Node[]
}

export type Actor = 'you' | 'manager' | 'operators' | 'reviewers' | 'automatic'

export const ACTORS: { id: Actor; label: string }[] = [
  { id: 'you', label: 'You' },
  { id: 'manager', label: 'Commander' },
  { id: 'operators', label: 'Agents' },
  { id: 'reviewers', label: 'Reviewers' },
  { id: 'automatic', label: 'Automatic' },
]

export interface FlowStep {
  title: string
  actor: Actor
  checkpoint: boolean
  done: boolean
  criteria: string
  who?: string // reviewers' names, on a reviewers' checkpoint
  now: boolean // where the work is: in progress
  change?: 'add' | 'remove' | 'edit' // what this pitch proposes to change
  // From a derived diagram: the step's id, its state in the board's words and
  // colour class, a line of detail (who, progress), a track the card opens.
  id?: string
  kind?: string
  text?: string
  meta?: string
  track?: boolean
  inside?: string // the title of the step this one is nested under (drawn indented in its box)
  noActor?: boolean // nothing in beads says whose move it is: drawn on the Commander's line, dashed
  review?: ReviewBlock // a plugged review block: one card, its checks open on a click
}

function actorOf(who: string | undefined, checkpoint: boolean): Actor {
  const w = (who ?? '').toLowerCase()
  if (w === 'user' || w === 'you') return 'you'
  if (w.startsWith('manager')) return 'manager'
  if (/^(automatic|auto|ci)$/.test(w)) return 'automatic'
  if (w.startsWith('operator') || w === 'agent' || w === 'agents') return 'operators'
  if (!w) return checkpoint ? 'you' : 'operators'
  return checkpoint ? 'reviewers' : 'operators'
}

/** flowSteps reads a workflow graph in order: checkpoints on their approver's line, work on its doer's. */
export function flowSteps(spec: unknown): { actors: typeof ACTORS; steps: FlowStep[] } {
  const steps: FlowStep[] = []
  const walk = (nodes: unknown) => {
    for (const n of Array.isArray(nodes) ? (nodes as Node[]) : []) {
      if (n.children?.length && !n.gate) {
        walk(n.children)
        continue
      }
      const checkpoint = !!n.gate
      const approvers = n.approvers ?? []
      const actor = actorOf(checkpoint ? approvers[0] : (n.by ?? n.owner), checkpoint)
      steps.push({
        title: n.title ?? '',
        actor,
        checkpoint,
        done: n.state === 'done',
        criteria: checkpoint ? (n.criteria ?? n.means ?? '') : '',
        who: actor === 'reviewers' ? approvers.join(', ') : undefined,
        now: n.state === 'in-progress' || n.state === 'review',
        change: n.change === 'add' || n.change === 'remove' || n.change === 'edit' ? n.change : undefined,
      })
    }
  }
  walk((spec as { nodes?: unknown })?.nodes)
  const used = new Set(steps.map((s) => s.actor))
  return { actors: ACTORS.filter((a) => used.has(a.id)), steps }
}

/** UNTITLED stands in for a step with no title: a bead id is never shown. */
export const UNTITLED = 'Untitled step'

const CHANGE_WORDS = { add: 'Proposed: new', remove: 'Proposed: drop', edit: 'Proposed: change' } as const

type View = 'changes' | 'before' | 'after'

/** viewSteps is the workflow as approved (before), as proposed (after), or both with the changes marked. */
export function viewSteps(steps: FlowStep[], view: View): FlowStep[] {
  if (view === 'before') return steps.filter((s) => s.change !== 'add').map((s) => ({ ...s, change: undefined }))
  if (view === 'after') return steps.filter((s) => s.change !== 'remove')
  return steps
}

export function ExecutionFlow({ spec }: { spec: unknown }) {
  const all = flowSteps(spec)
  const [view, setView] = useState<View>('changes')
  const changes = all.steps.filter((s) => s.change).length
  const steps = viewSteps(all.steps, changes ? view : 'changes')
  if (!steps.some((s) => s.checkpoint)) return <ChatGraph spec={spec} />
  return (
    <div className="xf">
      {changes > 0 && (
        <div className="xf-views">
          <div className="xf-toggle" role="tablist" aria-label="Workflow">
            {(['before', 'changes', 'after'] as const).map((v) => (
              <button key={v} type="button" role="tab" aria-selected={view === v} className={view === v ? 'on' : ''} onClick={() => setView(v)}>
                {v === 'before' ? 'Before' : v === 'after' ? 'After' : 'Changes'}
              </button>
            ))}
          </div>
          <p className="xf-legend">
            {view === 'before'
              ? 'The workflow as you approved it.'
              : view === 'after'
                ? `The workflow if you say yes, with the ${changes === 1 ? 'change' : 'changes'} highlighted.`
                : `This pitch's ${changes === 1 ? 'change' : `${changes} changes`} to the approved workflow: green is new, struck through is dropped, amber is changed.`}
          </p>
        </div>
      )}
      <SequenceDiagram steps={steps} />
    </div>
  )
}

/** SequenceDiagram is the lifelines, the steps on them going down and an arrow at each handoff. */
export function SequenceDiagram({ steps, onOpen, ensure = [] }: { steps: FlowStep[]; onOpen?: (id: string) => void; ensure?: Actor[] }) {
  const used = new Set([...steps.map((s) => s.actor), ...ensure])
  const actors = ACTORS.filter((a) => used.has(a.id))
  const col = (a: Actor) => actors.findIndex((x) => x.id === a) + 1
  // A card stays inside its own column and wraps; it never reaches under another line.
  const grow = '100%'
  const edge = (a: Actor) => (col(a) === 1 ? ' at-start' : col(a) === actors.length ? ' at-end' : '')
  // The current step is the one highlighted card: the first open checkpoint is
  // only marked "next" when nothing is current, else it is a muted future one.
  const next = steps.some((s) => s.now && !s.done) ? -1 : steps.findIndex((s) => s.checkpoint && !s.done)
  const rows: ReactElement[] = []
  steps.forEach((s, i) => {
    const prev = steps[i - 1]
    if (prev && prev.actor !== s.actor) {
      const from = col(prev.actor)
      const to = col(s.actor)
      const span = Math.abs(to - from) + 1
      rows.push(
        <div key={`h${i}`} className={`xf-handoff ${to > from ? 'right' : 'left'}`} style={{ gridRow: rows.length + 2, gridColumn: `${Math.min(from, to)} / span ${span}`, ['--span' as string]: span }}>
          <span className="xf-arrow" />
        </div>,
      )
    }
    // One line on top says where the step is: its state in its own words (the
    // user's checkpoint: "Needs your approval"), the last step marked Final.
    const last = i === steps.length - 1 && !s.done
    let top: string | null
    if (s.done) top = s.checkpoint ? '✓ Passed' : null
    else if (s.text) top = s.kind === 'needs' ? s.text.replace(/^Needs you\b/, 'Needs your approval') : s.text
    else if (s.checkpoint) top = s.now ? 'Now · checkpoint' : i === next ? 'Next checkpoint' : 'Checkpoint'
    else top = s.now ? 'Now' : null
    if (last && top) top = `Final: ${top}`
    else if (last) top = 'Final'
    const cls = `xf-step${s.kind ? ` kind-${s.kind}` : ''}${s.checkpoint ? ' is-check' : ''}${s.done ? ' is-done' : ''}${i === next ? ' is-next' : ''}${s.now ? ' is-now' : ''}${!s.done && !s.now && i !== next && (s.kind ? s.kind === 'idle' : s.checkpoint) ? ' is-future' : ''}${s.change ? ` is-${s.change}` : ''}${s.noActor ? ' is-noactor' : ''}${s.inside ? ' is-inside' : ''}${s.review ? ' is-review' : ''}${edge(s.actor)}`
    const style = { gridRow: rows.length + 2, gridColumn: col(s.actor), ['--grow' as string]: grow }
    const body = (
      <>
        {s.inside && s.inside !== steps[i - 1]?.inside && <span className="xf-kicker xf-inside">Part of: {clip(s.inside, 40)}</span>}
        {s.change && <span className="xf-change">{CHANGE_WORDS[s.change]}</span>}
        {top && <span className={`xf-kicker${s.text && !s.done ? ` xf-top agent-${s.kind}` : ''}`}>{top}</span>}
        <span className="xf-title">{s.title.trim() || UNTITLED}</span>
        {s.checkpoint && s.who && s.who !== 'you approve' && <span className="xf-who">{s.who}</span>}
        {s.meta && !s.done && s.kind !== 'needs' && <span className="xf-meta">{s.meta}</span>}
        {s.review && <ReviewChecks review={s.review} />}
        {s.criteria && !s.done && (
          <details className="xf-more" onClick={(e) => e.stopPropagation()}>
            <summary>Read more</summary>
            <span className="xf-criteria">{s.criteria}</span>
          </details>
        )}
      </>
    )
    rows.push(
      s.track && s.id && onOpen && !s.done ? (
        <button key={`s${i}`} type="button" className={`${cls} xf-open`} data-actor={s.actor} style={style} title={s.id} onClick={() => onOpen(s.id!)}>
          {body}
        </button>
      ) : (
        <div key={`s${i}`} className={cls} data-actor={s.actor} style={style} title={s.id}>
          {body}
        </div>
      ),
    )
  })
  return (
    // A last row takes the space left, so the lines run to the bottom of the pane.
    <div className="xf-grid" style={{ ['--cols' as string]: actors.length, gridTemplateRows: `auto repeat(${rows.length}, auto) 1fr` }}>
      {/* Behind the lane headers: one solid band the steps scroll under. */}
      <div className="xf-band" style={{ gridRow: 1, gridColumn: '1 / -1' }} aria-hidden />
      {actors.map((a, i) => (
        <div key={a.id} className="xf-actor" data-actor={a.id} style={{ gridRow: 1, gridColumn: i + 1 }}>
          {a.label}
        </div>
      ))}
      {actors.map((a, i) => (
        <div key={`l${a.id}`} className="xf-life" data-actor={a.id} style={{ gridColumn: i + 1, gridRow: `2 / span ${rows.length + 1}` }} aria-hidden />
      ))}
      {rows}
    </div>
  )
}

/** clip cuts a title to n characters with an ellipsis. */
function clip(t: string, n: number): string {
  return t.length > n ? `${t.slice(0, n - 1).trimEnd()}…` : t
}
