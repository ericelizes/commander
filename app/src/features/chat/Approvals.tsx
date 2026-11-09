import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { toolLabel, type PermissionSuggestion, type RequestItem } from './events'
import { Icon } from './icons'
import { Markdown } from './Markdown'
import { DiffView, editDiff } from './ToolCard'
import { useActions } from './context'

type Obj = Record<string, unknown>
const obj = (v: unknown): Obj => (v && typeof v === 'object' ? (v as Obj) : {})
const s = (v: unknown): string => (typeof v === 'string' ? v : '')

// alwaysSuggestions picks what "Always allow" applies: the narrow rule/dir
// suggestions when Claude offered any, otherwise everything it offered.
export function alwaysSuggestions(sugs: PermissionSuggestion[]): PermissionSuggestion[] {
  const narrow = sugs.filter((x) => x.type === 'addRules' || x.type === 'addDirectories')
  return narrow.length ? narrow : sugs
}

export function describeSuggestion(x: PermissionSuggestion): string {
  const where = x.destination === 'session' ? 'this session' : x.destination === 'localSettings' ? 'local settings' : x.destination === 'projectSettings' ? 'project settings' : x.destination === 'userSettings' ? 'user settings' : (x.destination ?? '')
  if (x.type === 'addRules' || x.type === 'replaceRules') {
    const rules = (x.rules ?? []).map((r) => (r.ruleContent ? `${r.toolName}(${r.ruleContent})` : r.toolName)).join(', ')
    return `${x.behavior ?? 'allow'} ${rules} · ${where}`
  }
  if (x.type === 'addDirectories') return `access ${(x.directories ?? []).join(', ')} · ${where}`
  if (x.type === 'setMode') return `mode ${x.mode} · ${where}`
  return `${x.type} · ${where}`
}

function useAutoFocus(enabled: boolean) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!enabled || !ref.current) return
    const active = document.activeElement as HTMLTextAreaElement | null
    // Don't steal focus from a half-typed message.
    if (active && active.tagName === 'TEXTAREA' && active.value.trim()) return
    ref.current.focus({ preventScroll: true })
  }, [enabled])
  return ref
}

function ResolvedRow({ item }: { item: RequestItem }) {
  const r = item.resolved!
  const what = item.type === 'plan' ? 'plan' : item.type === 'question' ? 'question' : toolLabel(item.tool)
  const verb = item.type === 'question' ? 'Answered' : item.type === 'plan' ? (r.behavior === 'allow' ? 'Approved' : 'Kept planning on') : r.behavior === 'allow' ? (r.always ? 'Always allowed' : 'Allowed') : 'Denied'
  return (
    <div className={`cc-row cc-resolved cc-resolved-${r.behavior}`}>
      <Icon name={r.behavior === 'allow' ? 'check' : 'x'} size={12} />
      <span>
        {verb} {what}
        {r.message ? <span className="cc-muted"> · {r.message}</span> : null}
      </span>
    </div>
  )
}

function Preview({ item }: { item: RequestItem }) {
  const input = obj(item.input)
  if (item.tool === 'Bash')
    return (
      <div className="cc-cmd">
        <span className="cc-prompt">$</span>
        <code>{s(input.command)}</code>
      </div>
    )
  if (item.tool === 'Edit' || item.tool === 'Write' || item.tool === 'MultiEdit') {
    return (
      <>
        <div className="cc-path">{s(input.file_path)}</div>
        <DiffView lines={editDiff(item.tool, input, {})} max={60} />
      </>
    )
  }
  if (item.tool === 'WebFetch') return <div className="cc-path">{s(input.url)}</div>
  return <pre className="cc-json">{JSON.stringify(input, null, 2)}</pre>
}

export function PermissionCard({ item }: { item: RequestItem }) {
  const { approve, readonly } = useActions()
  const [denying, setDenying] = useState(false)
  const [reason, setReason] = useState('')
  const ref = useAutoFocus(!item.resolved && !readonly)
  const reasonRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (denying) reasonRef.current?.focus()
  }, [denying])
  if (item.resolved) return <ResolvedRow item={item} />
  const always = alwaysSuggestions(item.suggestions)
  const allow = (alwaysAllow: boolean) =>
    approve({ id: item.requestId, behavior: 'allow', updatedInput: item.input, updatedPermissions: alwaysAllow ? always : undefined, always: alwaysAllow })
  const deny = () => approve({ id: item.requestId, behavior: 'deny', message: reason.trim() || 'Denied by user' })
  const onKey = (e: KeyboardEvent) => {
    if (readonly || e.target !== e.currentTarget) return
    const k = e.key.toLowerCase()
    if (k === '1' || k === 'y' || k === 'enter') allow(false)
    else if ((k === '2' || k === 'a') && always.length) allow(true)
    else if (k === '3' || k === 'n' || k === 'escape') setDenying(true)
    else return
    e.preventDefault()
  }
  return (
    <div className="cc-row">
      <div ref={ref} className="cc-approval" tabIndex={0} onKeyDown={onKey} role="group" aria-label={`Permission request for ${item.tool}`}>
        <div className="cc-approval-head">
          <Icon name="shield" size={15} />
          <span className="cc-approval-title">
            Allow <b>{toolLabel(item.tool)}</b>?
          </span>
          {item.description && item.description !== s(obj(item.input).command) && <span className="cc-approval-desc">{item.description}</span>}
        </div>
        <div className="cc-approval-body">
          <Preview item={item} />
          {item.blockedPath && (
            <div className="cc-muted">
              Touches <code>{item.blockedPath}</code>
            </div>
          )}
        </div>
        {denying ? (
          <form
            className="cc-approval-actions"
            onSubmit={(e) => {
              e.preventDefault()
              deny()
            }}
          >
            <input
              ref={reasonRef}
              className="cc-input"
              placeholder="Why? (optional, sent to the agent)"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') {
                  setDenying(false)
                  ref.current?.focus()
                }
              }}
            />
            <button type="submit" className="cc-btn cc-btn-danger">
              Deny
            </button>
          </form>
        ) : (
          <div className="cc-approval-actions">
            <button type="button" className="cc-btn cc-btn-primary" disabled={readonly} onClick={() => allow(false)}>
              Allow once <kbd>1</kbd>
            </button>
            {always.length > 0 && (
              <button type="button" className="cc-btn" disabled={readonly} onClick={() => allow(true)} title={always.map(describeSuggestion).join('\n')}>
                Always allow <kbd>2</kbd>
              </button>
            )}
            <button type="button" className="cc-btn cc-btn-ghost-danger" disabled={readonly} onClick={() => setDenying(true)}>
              Deny <kbd>3</kbd>
            </button>
            {always.length > 0 && <span className="cc-approval-rule">{describeSuggestion(always[0])}</span>}
          </div>
        )}
      </div>
    </div>
  )
}

const OTHER = '__other__'

export function QuestionCard({ item }: { item: RequestItem }) {
  const { answer, readonly } = useActions()
  const [picked, setPicked] = useState<Record<number, string[]>>({})
  const [other, setOther] = useState<Record<number, string>>({})
  const ref = useAutoFocus(!item.resolved && !readonly)
  if (item.resolved) return <ResolvedRow item={item} />
  const toggle = (qi: number, label: string, multi: boolean) =>
    setPicked((p) => {
      const cur = p[qi] ?? []
      const next = multi ? (cur.includes(label) ? cur.filter((x) => x !== label) : [...cur, label]) : [label]
      return { ...p, [qi]: next }
    })
  const valueFor = (qi: number) =>
    (picked[qi] ?? [])
      .map((l) => (l === OTHER ? other[qi]?.trim() ?? '' : l))
      .filter(Boolean)
      .join(', ')
  const complete = item.questions.every((_, qi) => valueFor(qi))
  const submit = () => {
    if (!complete || readonly) return
    const answers: Record<string, string> = {}
    item.questions.forEach((q, qi) => (answers[q.question] = valueFor(qi)))
    answer(item.requestId, answers)
  }
  const onKey = (e: KeyboardEvent) => {
    if (e.target !== e.currentTarget || item.questions.length !== 1) return
    const n = Number(e.key)
    const q = item.questions[0]
    if (n >= 1 && n <= q.options.length) {
      toggle(0, q.options[n - 1].label, !!q.multiSelect)
      e.preventDefault()
    } else if (e.key === 'Enter') {
      submit()
      e.preventDefault()
    }
  }
  return (
    <div className="cc-row">
      <div ref={ref} className="cc-approval cc-question" tabIndex={0} onKeyDown={onKey} role="group" aria-label="Question from the agent">
        <div className="cc-approval-head">
          <Icon name="question" size={15} />
          <span className="cc-approval-title">The agent has {item.questions.length > 1 ? `${item.questions.length} questions` : 'a question'}</span>
        </div>
        {item.questions.map((q, qi) => {
          const multi = !!q.multiSelect
          return (
            <fieldset key={qi} className="cc-q">
              <legend>
                {q.header && <span className="cc-chip">{q.header}</span>}
                <span className="cc-q-text">{q.question}</span>
                {multi && <span className="cc-muted"> · pick any</span>}
              </legend>
              <div className="cc-options">
                {q.options.map((o, oi) => {
                  const on = (picked[qi] ?? []).includes(o.label)
                  return (
                    <label key={o.label} className={`cc-option${on ? ' cc-on' : ''}`}>
                      <input type={multi ? 'checkbox' : 'radio'} name={`q-${item.id}-${qi}`} checked={on} disabled={readonly} onChange={() => toggle(qi, o.label, multi)} />
                      <span className="cc-option-key">{oi + 1}</span>
                      <span className="cc-option-text">
                        <span className="cc-option-label">{o.label}</span>
                        {o.description && <span className="cc-option-desc">{o.description}</span>}
                      </span>
                    </label>
                  )
                })}
                <label className={`cc-option cc-option-other${(picked[qi] ?? []).includes(OTHER) ? ' cc-on' : ''}`}>
                  <input type={multi ? 'checkbox' : 'radio'} name={`q-${item.id}-${qi}`} checked={(picked[qi] ?? []).includes(OTHER)} disabled={readonly} onChange={() => toggle(qi, OTHER, multi)} />
                  <input
                    className="cc-input"
                    placeholder="Other…"
                    aria-label="Other answer"
                    value={other[qi] ?? ''}
                    disabled={readonly}
                    onFocus={() => !(picked[qi] ?? []).includes(OTHER) && toggle(qi, OTHER, multi)}
                    onChange={(e) => setOther((o) => ({ ...o, [qi]: e.target.value }))}
                    onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), submit())}
                  />
                </label>
              </div>
            </fieldset>
          )
        })}
        <div className="cc-approval-actions">
          <button type="button" className="cc-btn cc-btn-primary" disabled={!complete || readonly} onClick={submit}>
            Submit answer
          </button>
        </div>
      </div>
    </div>
  )
}

export function PlanCard({ item }: { item: RequestItem }) {
  const { approve, readonly } = useActions()
  const [feedback, setFeedback] = useState('')
  const [keep, setKeep] = useState(false)
  const ref = useAutoFocus(!item.resolved && !readonly)
  return (
    <div className="cc-row">
      <div ref={ref} className={`cc-approval cc-plan${item.resolved ? ' cc-plan-resolved' : ''}`} tabIndex={-1} role="group" aria-label="Proposed plan">
        <div className="cc-approval-head">
          <Icon name="map" size={15} />
          <span className="cc-approval-title">Proposed plan</span>
          {item.resolved && <span className={`cc-badge ${item.resolved.behavior === 'allow' ? 'cc-badge-ok' : ''}`}>{item.resolved.behavior === 'allow' ? 'approved' : 'kept planning'}</span>}
        </div>
        <div className="cc-plan-body">
          <Markdown text={item.plan} />
        </div>
        {!item.resolved &&
          (keep ? (
            <form
              className="cc-approval-actions"
              onSubmit={(e) => {
                e.preventDefault()
                approve({ id: item.requestId, behavior: 'deny', message: feedback.trim() || 'Keep planning.' })
              }}
            >
              <input autoFocus className="cc-input" placeholder="What should change in the plan?" value={feedback} onChange={(e) => setFeedback(e.target.value)} />
              <button type="submit" className="cc-btn">
                Send feedback
              </button>
            </form>
          ) : (
            <div className="cc-approval-actions">
              <button type="button" className="cc-btn cc-btn-primary" disabled={readonly} onClick={() => approve({ id: item.requestId, behavior: 'allow', updatedInput: { plan: item.plan } })}>
                Approve plan
              </button>
              <button type="button" className="cc-btn" disabled={readonly} onClick={() => setKeep(true)}>
                Keep planning
              </button>
            </div>
          ))}
      </div>
    </div>
  )
}
