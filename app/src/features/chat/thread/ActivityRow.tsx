import { BookOpen, Brain, ChevronDown, ChevronRight, Eye, LoaderCircle, ShieldAlert, Sparkles, Zap, type LucideIcon } from 'lucide-react'
import { useState } from 'react'
import { ToolDetail } from '../ToolCard'
import { describeStep, stepFailed, stepMode, stepRunning, thoughtParts, type StepMode } from './describeStep'
import { duration, SwapText, useHeld, useNow } from './motion'
import type { Activity, Step } from './toThread'

const MODE_ICON: Record<StepMode, LucideIcon> = { thinking: Brain, reading: BookOpen, looking: Eye, changing: Zap }

function worked(ms: number) {
  return ms < 5000 ? 'Worked for a few seconds' : `Worked for ${duration(ms)}`
}

/**
 * ActivityRow is everything the agent does between two pieces of its reply.
 * While it works, the row is the current step (icon, words and timer change
 * with it); when done it settles into "Worked for Ns", and the dropdown holds
 * the timeline.
 */
export function ActivityRow({ data }: { data: Activity }) {
  const { steps, active, startedAt, endedAt } = data
  const [open, setOpen] = useState(false)
  const current = steps[steps.length - 1]
  const mode = stepMode(current)
  const awaiting = steps.some((s) => s.type === 'tool' && s.awaiting)

  const failed = steps.filter(stepFailed).length

  // "Worked for" is what the recorded event times say, never the wall clock:
  // a row first seen while history loads would otherwise count from hours ago.
  const doneLabel = startedAt !== undefined && endedAt !== undefined ? worked(endedAt - startedAt) : 'Worked on it'
  const liveLabel = awaiting ? 'Waiting for your approval' : active ? describeStep(current).activeLabel : doneLabel
  // While it runs, the icon and words for the current step change together and
  // at most once per HOLD_MS (steps can flip many times a second); once done,
  // the summary shows at once.
  const shown = useHeld({ mode, label: liveLabel }, `${awaiting}|${active}|${mode}|${liveLabel}`)
  const label = active && !awaiting ? shown.label : liveLabel
  const shownMode = active && !awaiting ? shown.mode : mode
  const Icon = awaiting ? ShieldAlert : active ? MODE_ICON[shownMode] : Sparkles
  const now = useNow(active)

  return (
    <div className="ct-activity">
      <button type="button" className={`ct-activity-head${open ? ' open' : ''}${awaiting ? ' attn' : ''}`} onClick={() => setOpen(!open)} aria-expanded={open}>
        <Icon key={awaiting ? 'attn' : active ? shownMode : 'done'} size={14} className="ct-icon-swap" />
        <SwapText text={label} className={active && !awaiting ? 'ct-shimmer' : ''} />
        {active && startedAt !== undefined && <span className="ct-elapsed">· {duration(now - startedAt)}</span>}
        {!active && failed > 0 && <span className="ct-failed">· {failed} failed</span>}
        <ChevronDown size={14} className="ct-chevron" />
      </button>
      {open && (
        <ol className="ct-steps">
          {steps.map((step, i) => (
            <StepRow key={step.id} step={step} running={active && i === steps.length - 1 && stepRunning(step)} />
          ))}
        </ol>
      )}
    </div>
  )
}

function StepRow({ step, running }: { step: Step; running: boolean }) {
  const [open, setOpen] = useState(false)
  const { label, activeLabel, icon: Icon } = describeStep(step)
  const body = step.type === 'thinking' ? thoughtParts(step.text).body : ''
  const hasDetail = step.type === 'tool' || body !== ''
  return (
    <li className="ct-step">
      <span className="ct-step-dot">{running ? <LoaderCircle size={12} className="ct-spin" /> : <Icon size={12} />}</span>
      <button type="button" className={`ct-step-head${hasDetail ? '' : ' plain'}`} onClick={() => hasDetail && setOpen(!open)} aria-expanded={hasDetail ? open : undefined}>
        <span className={`ct-step-label${stepFailed(step) ? ' failed' : ''}`}>{running ? activeLabel : label}</span>
        {hasDetail && <ChevronRight size={14} className={`ct-step-chevron${open ? ' open' : ''}`} />}
      </button>
      {open && (
        <div className="ct-step-detail">
          {step.type === 'thinking' ? <div className="ct-thought">{body}</div> : <ToolDetail item={step} />}
        </div>
      )}
    </li>
  )
}
