// WorkflowView draws a workstream's (or track's) workflow exactly as cmdrd
// derived it (display row `workflow`): what is done, where it is now, and what
// approving the step being decided starts, through the next gate. Compact on a
// decision; with every step under it when opened from the sidebar. It derives
// no status of its own: every word and colour is the step's kind and text.

import { useState, type ReactNode } from 'react'
import type { Workflow, WorkflowStep } from '../../../shared/snapshot'
import { ReviewChecks } from './ReviewChecks'

export interface WorkflowParts {
  current: WorkflowStep | null
  within: Record<string, string> // step id -> the title of the step it sits in
  done: WorkflowStep[] // the top level's finished steps
  starts: WorkflowStep[] // what approving current starts, in the order it can begin
  other: WorkflowStep[] // the top level's other open steps
}

/** workflowParts splits w around focus (a decision's bead), else around w's own current step. */
export function workflowParts(w: Workflow, focus?: string): WorkflowParts {
  const byId = new Map<string, WorkflowStep>()
  const within: Record<string, string> = {}
  const index = (steps: WorkflowStep[], up?: WorkflowStep) => {
    for (const s of steps) {
      byId.set(s.id, s)
      if (up) within[s.id] = up.title
      index(s.children ?? [], s)
    }
  }
  index(w.steps)
  const current = (focus ? byId.get(focus) : w.current ? byId.get(w.current) : undefined) ?? null
  const ids = focus ? (current?.unlocks ?? []) : (w.unlocks ?? [])
  const starts = ids.map((id) => byId.get(id)).filter((s): s is WorkflowStep => !!s)
  const shown = new Set([current?.id, ...ids])
  return {
    current,
    within,
    done: w.steps.filter((s) => s.kind === 'done'),
    starts,
    other: w.steps.filter((s) => s.kind !== 'done' && !shown.has(s.id)),
  }
}

/** subWorkflow is a track's own workflow inside its workstream's, or null. */
export function subWorkflow(w: Workflow | undefined, id: string | undefined): Workflow | null {
  if (!w) return null
  if (!id) return w
  const s = findStep(w.steps, id)
  if (!s?.children?.length) return null
  const at = s.current ? findStep(s.children, s.current) : undefined
  return { current: s.current, unlocks: at?.unlocks, steps: s.children }
}

function findStep(steps: WorkflowStep[], id: string): WorkflowStep | undefined {
  for (const s of steps) {
    if (s.id === id) return s
    const hit = findStep(s.children ?? [], id)
    if (hit) return hit
  }
}

export function WorkflowView({ workflow, focus, full = false }: { workflow: Workflow; focus?: string; full?: boolean }) {
  const p = workflowParts(workflow, focus)
  if (!p.current && !full) return null
  const deciding = !!focus || p.current?.kind === 'needs'
  return (
    <section className={`wf${full ? ' wf-full' : ''}`} aria-label="Workflow">
      {!full && p.done.length > 0 && <Fold label={`${p.done.length} done`} steps={p.done} within={p.within} />}
      {p.current && (
        <div className="wf-group">
          <div className="wf-label">{deciding ? 'Deciding now' : 'Now'}</div>
          <ul className="wf-list">
            <Step s={p.current} within={p.within[p.current.id]} now />
          </ul>
        </div>
      )}
      {p.current && (p.current.gate || deciding) && (
        <div className="wf-group">
          <div className="wf-label">Approving starts</div>
          {p.starts.length > 0 ? (
            <ul className="wf-list">
              {p.starts.map((s) => (
                <Step key={s.id} s={s} within={p.within[s.id]} />
              ))}
            </ul>
          ) : (
            <p className="wf-none">Nothing in the plan waits on this.</p>
          )}
        </div>
      )}
      {!full && p.other.length > 0 && <Fold label={`${p.other.length} other open`} steps={p.other} within={p.within} />}
      {full && (
        <div className="wf-group">
          <div className="wf-label">Every step</div>
          <Tree steps={workflow.steps} current={p.current?.id} />
        </div>
      )}
    </section>
  )
}

// Fold is a count that opens to its steps.
function Fold({ label, steps, within }: { label: string; steps: WorkflowStep[]; within: Record<string, string> }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="wf-group">
      <button type="button" className="wf-fold" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="wf-caret" aria-hidden>
          {open ? '▾' : '▸'}
        </span>
        {label}
      </button>
      {open && (
        <ul className="wf-list">
          {steps.map((s) => (
            <Step key={s.id} s={s} within={within[s.id]} />
          ))}
        </ul>
      )}
    </div>
  )
}

// Tree is every step at one level, done ones folded into a count, open ones with their own steps under them.
function Tree({ steps, current }: { steps: WorkflowStep[]; current?: string }) {
  const done = steps.filter((s) => s.kind === 'done')
  const open = steps.filter((s) => s.kind !== 'done')
  return (
    <>
      {done.length > 0 && <Fold label={`${done.length} done`} steps={done} within={{}} />}
      {open.length > 0 && (
        <ul className="wf-list">
          {open.map((s) => (
            <Step key={s.id} s={s} now={s.id === current}>
              {!s.review && (s.children?.length ?? 0) > 0 && (
                <div className="wf-sub">
                  <Tree steps={s.children!} current={s.current} />
                </div>
              )}
            </Step>
          ))}
        </ul>
      )}
    </>
  )
}

// Step is one row, the same slots on every row: dot, title, and its state (with who approves a gate).
function Step({ s, within, now = false, children }: { s: WorkflowStep; within?: string; now?: boolean; children?: ReactNode }) {
  return (
    <li className={`wf-step agent-${s.kind}${now ? ' is-now' : ''}`}>
      <div className="wf-row" title={s.id}>
        <span className="wf-title">
          {s.title}
          {within && <span className="wf-within"> · {within}</span>}
        </span>
        <span className="wf-text" title={s.why}>
          {s.text}
          {s.gate && s.who && <span className="wf-who"> · gate, {s.who === 'you' ? 'you approve' : `${s.who} approves`}</span>}
          {s.review && <span className="wf-who"> · {s.review.done} of {s.review.total} checks done</span>}
        </span>
      </div>
      {s.review && <ReviewChecks review={s.review} />}
      {children}
    </li>
  )
}
