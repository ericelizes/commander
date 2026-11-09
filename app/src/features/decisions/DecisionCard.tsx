import { splitTickets } from '../linear/Ticket'
import { useEffect, useId, useRef, useState } from 'react'
import type { KeyboardEvent, ReactNode, RefObject } from 'react'
import { answerMode, askWhere, copyText, paragraphs, readyChoice, resolveNeeds, validationHint } from './logic'
import type { CommanderApi, DecisionOutcome, NeedsYouItem, ResolveAction } from './types'
import { useToast } from './useToast'
import './decisions.css'

export interface DecisionCardProps {
  needs: NeedsYouItem
  api: CommanderApi
  // Called once the flag is resolved (or found already answered elsewhere).
  onDone?: (outcome: DecisionOutcome) => void
  // The host saw this flag leave the snapshot: show "no longer needed".
  gone?: boolean
  // Focus the first control on mount (default true).
  autoFocus?: boolean
  // Renders the context (the host passes markdown); default is plain paragraphs.
  renderContext?: (text: string) => ReactNode
}

type Phase =
  | { name: 'idle' }
  | { name: 'sending', action: ResolveAction }
  | { name: 'error', message: string }
  | { name: 'sent', action: ResolveAction, choice?: string, note?: string }
  | { name: 'gone' }

// The form the user answers a ⚑ with: the Manager's question, context, options
// (1-9, with implications), commands to copy and a note. Submitting runs
// internal-resolve-needs, which mails the Manager and clears the flag.
export function DecisionCard({ needs, api, onDone, gone, autoFocus = true, renderContext }: DecisionCardProps) {
  const d = needs.decision
  const mode = d ? answerMode(d) : null
  const options = d?.options ?? []
  const steps = d?.steps ?? []
  const [selected, setSelected] = useState(-1)
  const [note, setNote] = useState('')
  const [attempted, setAttempted] = useState(false)
  const [phase, setPhase] = useState<Phase>({ name: 'idle' })
  const { toast, show } = useToast()

  const ids = useId()
  const qid = `${ids}-q`
  const noteId = `${ids}-note`
  const hintId = `${ids}-hint`
  const rootRef = useRef<HTMLElement>(null)
  const radioRefs = useRef<(HTMLDivElement | null)[]>([])
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const primaryRef = useRef<HTMLButtonElement>(null)
  const doneRef = useRef<HTMLHeadingElement>(null)

  const shownPhase: Phase = gone && phase.name === 'idle' ? { name: 'gone' } : phase
  const busy = phase.name === 'sending'
  const primarySending = phase.name === 'sending' && phase.action === (d ? 'decide' : 'approve')
  const choice = d ? readyChoice(d, selected, note) : 'approve'

  useEffect(() => {
    if (!autoFocus) return
    if (options.length > 0) radioRefs.current[0]?.focus()
    else if (mode === 'input') noteRef.current?.focus()
    else primaryRef.current?.focus()
    // Only on mount: later focus follows the user.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (shownPhase.name === 'sent' || shownPhase.name === 'gone') doneRef.current?.focus()
  }, [shownPhase.name])

  async function run(action: ResolveAction, choiceArg?: string) {
    if (busy) return
    setPhase({ name: 'sending', action })
    const text = action === 'decide' ? note.trim() : ''
    const res = await resolveNeeds(api, needs, action, choiceArg, text)
    if (res.status === 'ok') {
      setPhase({ name: 'sent', action, choice: choiceArg, note: text || undefined })
      onDone?.({ kind: 'answered', action, choice: choiceArg, note: text || undefined })
    } else if (res.status === 'gone') {
      setPhase({ name: 'gone' })
      onDone?.({ kind: 'gone' })
    } else {
      setPhase({ name: 'error', message: res.message })
    }
  }

  function submit() {
    if (!d) return run('approve')
    if (choice === null) {
      setAttempted(true)
      if (mode === 'input') noteRef.current?.focus()
      else radioRefs.current[0]?.focus()
      return
    }
    return run('decide', choice)
  }

  function select(i: number) {
    if (i < 0 || i >= options.length) return
    setSelected(i)
    setAttempted(false)
    radioRefs.current[i]?.focus()
  }

  async function copy(text: string, what: string) {
    try {
      await copyText(api, text)
      show(`Copied ${what}`)
    } catch (err) {
      show(`Copy failed: ${err instanceof Error ? err.message : String(err)}`, 'err')
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLElement>) {
    if (shownPhase.name === 'sent' || shownPhase.name === 'gone') return
    const target = e.target as HTMLElement
    const inText = target.tagName === 'TEXTAREA' || target.tagName === 'INPUT'
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      submit()
      return
    }
    if (inText) {
      if (e.key === 'Escape') {
        e.preventDefault()
        rootRef.current?.focus()
      }
      return
    }
    if (e.metaKey || e.ctrlKey || e.altKey) return
    const current = options.length > 0 ? radioRefs.current.findIndex(r => r === target) : -1
    if (/^[1-9]$/.test(e.key) && options.length > 0) {
      e.preventDefault()
      select(Number(e.key) - 1)
      return
    }
    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowRight':
      case 'j':
        if (options.length > 0) {
          e.preventDefault()
          select(Math.min(options.length - 1, (current >= 0 ? current : selected) + 1))
        }
        return
      case 'ArrowUp':
      case 'ArrowLeft':
      case 'k':
        if (options.length > 0) {
          e.preventDefault()
          select(Math.max(0, (current >= 0 ? current : selected) - 1))
        }
        return
      case ' ':
        if (current >= 0) {
          e.preventDefault()
          select(current)
        }
        return
      case 'n':
      case 'i':
        if (d) {
          e.preventDefault()
          noteRef.current?.focus()
        }
        return
      case 'c':
        if (steps.length > 0) {
          e.preventDefault()
          copy(steps.join('\n'), steps.length > 1 ? `${steps.length} commands` : 'command')
        }
        return
      case 'Enter':
        if (target.tagName !== 'BUTTON') {
          e.preventDefault()
          submit()
        }
    }
  }

  const question = d?.question || needs.ask || needs.title || needs.bead_id
  const context = d ? d.context : needs.note

  return (
    <section
      ref={rootRef}
      className="cmdr-dec"
      aria-labelledby={qid}
      tabIndex={-1}
      onKeyDown={onKeyDown}
      data-phase={shownPhase.name}
    >
      <Eyebrow needs={needs} phase={shownPhase} />

      {shownPhase.name === 'sent' && (
        <Confirmation phase={shownPhase} headingRef={doneRef} headingId={qid} question={question} />
      )}
      {shownPhase.name === 'gone' && (
        <div className="dec-done dec-done-gone">
          <h2 id={qid} ref={doneRef} tabIndex={-1} className="dec-done-title">No longer needed</h2>
          <p className="dec-muted">
            “{question}” was answered or withdrawn elsewhere, so there is nothing left to decide.
          </p>
        </div>
      )}

      {(shownPhase.name === 'idle' || shownPhase.name === 'sending' || shownPhase.name === 'error') && (
        <>
          <h2 id={qid} className="dec-question">
            {splitTickets(question).text}
          </h2>
          {needs.stale_why && <p className="dec-context">{needs.stale_why}</p>}
          {renderContext && context?.trim()
            ? <div className="dec-context dec-context-rich">{renderContext(context)}</div>
            : paragraphs(context).map((p, i) => <p key={i} className="dec-context">{p}</p>)}

          {options.length > 0 && (
            <div
              role="radiogroup"
              aria-labelledby={qid}
              aria-required="true"
              aria-describedby={choice === null ? hintId : undefined}
              className="dec-options"
            >
              {options.map((o, i) => {
                const checked = i === selected
                const focusable = selected >= 0 ? checked : i === 0
                const implId = `${ids}-impl-${i}`
                return (
                  <div
                    key={i}
                    ref={el => { radioRefs.current[i] = el }}
                    role="radio"
                    aria-checked={checked}
                    aria-describedby={o.implications ? implId : undefined}
                    aria-disabled={busy || undefined}
                    tabIndex={focusable ? 0 : -1}
                    className={`dec-option${checked ? ' is-selected' : ''}${o.recommended ? ' is-recommended' : ''}`}
                    onClick={() => !busy && select(i)}
                  >
                    <kbd className="dec-key" aria-hidden="true">{i + 1}</kbd>
                    <div className="dec-option-body">
                      <div className="dec-option-label">
                        <span>{o.label}</span>
                        {o.recommended && <span className="dec-badge">★ Recommended</span>}
                      </div>
                      {o.implications && <div id={implId} className="dec-option-impl">{o.implications}</div>}
                    </div>
                    <span className="dec-radio" aria-hidden="true" />
                  </div>
                )
              })}
            </div>
          )}

          {steps.length > 0 && (
            <div className="dec-steps">
              <div className="dec-section-head">
                <span id={`${ids}-steps`}>Do this</span>
                {steps.length > 1 && (
                  <button type="button" className="dec-link" onClick={() => copy(steps.join('\n'), `${steps.length} commands`)}>
                    Copy all
                  </button>
                )}
              </div>
              <ol aria-labelledby={`${ids}-steps`}>
                {steps.map((s, i) => (
                  <li key={i} className="dec-step">
                    <span className="dec-step-n" aria-hidden="true">{i + 1}</span>
                    <pre><code>{s}</code></pre>
                    <button
                      type="button"
                      className="dec-copy"
                      aria-label={`Copy command ${i + 1}`}
                      onClick={() => copy(s, 'command')}
                    >
                      <CopyIcon /> Copy
                    </button>
                  </li>
                ))}
              </ol>
            </div>
          )}

          {d && (
            <div className="dec-note">
              <label htmlFor={noteId} className="dec-note-label">
                {mode === 'input' ? 'Your answer' : <>Add a note <span className="dec-muted">(optional)</span></>}
              </label>
              <textarea
                id={noteId}
                ref={noteRef}
                value={note}
                rows={mode === 'input' ? 4 : 2}
                placeholder={mode === 'input' ? 'Type your answer for the Manager…' : 'Anything the Manager should know'}
                aria-required={mode === 'input' || undefined}
                aria-describedby={mode === 'input' && choice === null ? hintId : undefined}
                disabled={busy}
                onChange={e => {
                  setNote(e.target.value)
                  setAttempted(false)
                }}
              />
            </div>
          )}

          {shownPhase.name === 'error' && (
            <div role="alert" className="dec-error">
              <strong>Could not send.</strong> {shownPhase.message}
            </div>
          )}

          <div className="dec-actions">
            <button
              ref={primaryRef}
              type="button"
              className="dec-primary"
              disabled={choice === null || busy}
              aria-describedby={choice === null ? hintId : undefined}
              onClick={() => submit()}
            >
              {primarySending && <span className="dec-spinner" aria-hidden="true" />}
              {primaryLabel(d ? mode : null, primarySending)}
            </button>
            <span id={hintId} className={`dec-hint${attempted ? ' is-warn' : ''}`}>
              {choice === null ? validationHint(d!) : <><kbd>Enter</kbd> to submit</>}
            </span>
            <span className="dec-spacer" />
            <button type="button" className="dec-secondary" disabled={busy} onClick={() => run('defer')} title="Park the step as not started">
              {phase.name === 'sending' && phase.action === 'defer' ? 'Deferring…' : 'Defer'}
            </button>
            {d && mode !== 'steps' && (
              <button type="button" className="dec-secondary" disabled={busy} onClick={() => run('approve')} title="Mark the step done without answering">
                {phase.name === 'sending' && phase.action === 'approve' ? 'Approving…' : 'Approve'}
              </button>
            )}
          </div>
        </>
      )}

      <div role="status" aria-live="polite" className={`dec-toast${toast ? ' is-shown' : ''}${toast?.tone === 'err' ? ' is-err' : ''}`}>
        {toast?.text}
      </div>
    </section>
  )
}

function primaryLabel(mode: ReturnType<typeof answerMode> | null, sending: boolean): string {
  if (mode === null) return sending ? 'Approving…' : 'Approve'
  if (sending) return 'Sending…'
  if (mode === 'steps') return 'Done'
  if (mode === 'input') return 'Send answer'
  return 'Submit'
}

function Eyebrow({ needs, phase }: { needs: NeedsYouItem, phase: Phase }) {
  const answered = phase.name === 'sent'
  return (
    <div className="dec-eyebrow">
      {answered
        ? <span className="dec-flag is-ok">✓ Answered</span>
        : phase.name === 'gone'
          ? <span className="dec-flag is-muted">⚑ Closed</span>
          : <span className="dec-flag">⚑ {needs.decision ? 'Decision' : 'Needs you'}</span>}
      {needs.workstream && <span className="dec-ws">{askWhere(needs)}</span>}
      <span className="dec-bead">{needs.bead_id}</span>
    </div>
  )
}

function Confirmation({ phase, headingRef, headingId, question }: {
  phase: Extract<Phase, { name: 'sent' }>
  headingRef: RefObject<HTMLHeadingElement | null>
  headingId: string
  question: string
}) {
  const title = phase.action === 'approve' ? 'Approved' : phase.action === 'defer' ? 'Deferred' : 'Answer sent'
  return (
    <div className={`dec-done${phase.action === 'defer' ? ' dec-done-defer' : ''}`}>
      <div className="dec-done-mark" aria-hidden="true">{phase.action === 'defer' ? '↷' : '✓'}</div>
      <h2 id={headingId} ref={headingRef} tabIndex={-1} className="dec-done-title">{title}</h2>
      <p className="dec-muted dec-done-q">{question}</p>
      {phase.action === 'decide' && (
        <>
          <p className="dec-done-choice">
            Sent to the Manager: <strong>{phase.choice}</strong>
          </p>
          {phase.note && <blockquote className="dec-done-note">{phase.note}</blockquote>}
          <p className="dec-muted">The flag is cleared and the step is back in progress; the Manager reads this by mail.</p>
        </>
      )}
      {phase.action === 'approve' && (
        <p className="dec-muted">The step is marked done and its Manager was told to carry on.</p>
      )}
      {phase.action === 'defer' && (
        <p className="dec-muted">The step is parked as not started; its Manager will not re-flag it unless something changes.</p>
      )}
    </div>
  )
}

function CopyIcon() {
  return (
    <svg width="12" height="12" viewBox="0 0 16 16" aria-hidden="true" focusable="false">
      <rect x="5" y="5" width="9" height="9" rx="2" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M3 11V3.5A1.5 1.5 0 0 1 4.5 2H11" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  )
}
