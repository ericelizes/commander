import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fmtCost, fmtTokens, type ChatState } from './events'
import type { TerminalSession } from './ChatPane'
import { Icon } from './icons'
import { modelName } from './modelName'

function Meter({ value, warnAt = 0.7 }: { value: number; warnAt?: number }) {
  const pct = Math.max(0, Math.min(1, value))
  return (
    <span className={`cc-meter${pct >= 0.9 ? ' cc-meter-crit' : pct >= warnAt ? ' cc-meter-warn' : ''}`}>
      <span className="cc-meter-track">
        <span className="cc-meter-fill" style={{ width: `${pct * 100}%` }} />
      </span>
      <span className="cc-meter-val">{Math.round(pct * 100)}%</span>
    </span>
  )
}

function resetIn(sec?: number): string {
  if (!sec) return ''
  const ms = sec * 1000 - Date.now()
  if (ms <= 0) return ''
  const h = Math.floor(ms / 3_600_000)
  const m = Math.round((ms % 3_600_000) / 60_000)
  return h ? `resets in ${h}h ${m}m` : `resets in ${m}m`
}

/** MenuItem is one ⋯ menu action. Items with a group render under its heading,
 * as a radio list: checked marks the current one (it does nothing), detail is
 * a second line, tone colours its dot (ok / warning / out). */
export interface MenuItem {
  label: string
  onSelect: () => void
  group?: string
  checked?: boolean
  detail?: string
  tone?: 'ok' | 'warning' | 'out' | 'unknown'
}

// The app's middle header has this slot; the chat's controls render there so
// the column has one header. Standalone (tests, the gallery) they render inline.
export const HEADER_SLOT_ID = 'chat-header-slot'

/**
 * Header is the chat's controls: what needs you (a terminal session to move,
 * a lost connection) and a ⋯ menu holding the model, context, plan usage, cost
 * and the session actions.
 */
export function Header({
  state,
  extraMenu,
  terminal,
  costNote,
}: {
  title?: string
  state: ChatState
  extraMenu?: MenuItem[]
  terminal?: TerminalSession
  costNote?: string // the role's spend over time (est.), shown in the menu button's tooltip
}) {
  const [menu, setMenu] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)
  const [slot, setSlot] = useState<HTMLElement | null>(null)
  useEffect(() => setSlot(document.getElementById(HEADER_SLOT_ID)), [])
  useEffect(() => {
    if (!menu) return
    const close = (e: MouseEvent) => !menuRef.current?.contains(e.target as Node) && setMenu(false)
    window.addEventListener('mousedown', close)
    return () => window.removeEventListener('mousedown', close)
  }, [menu])

  const five = state.rateLimit?.windows.five_hour
  const seven = state.rateLimit?.windows.seven_day
  const tokensUsed = state.usage.input + state.usage.output + state.usage.cacheRead + state.usage.cacheCreate
  const items = extraMenu ?? []
  const model = modelName(state.model)

  const controls = (
    <div className="cc-header">
      {terminal && (
        <>
          <span className="cc-badge cc-badge-terminal" title={terminal.title}>
            <Icon name="terminal" size={10} /> {terminal.label}
          </span>
          {terminal.onMove && (
            <button type="button" className="cc-move-btn" onClick={terminal.onMove}>
              Move to app
            </button>
          )}
        </>
      )}
      <div className="cc-menu" ref={menuRef}>
        <button
          type="button"
          className="cc-icon-btn"
          aria-label="Chat menu"
          title={[model, fmtCost(state.costUsd), costNote].filter(Boolean).join(' · ')}
          aria-expanded={menu}
          onClick={() => setMenu(!menu)}
        >
          <Icon name="more" size={16} />
        </button>
        {menu && (
          <div className="cc-menu-pop" role="menu">
            <dl className="cc-stats">
              {model && (
                <>
                  <dt>Model</dt>
                  <dd title={state.model}>{model}</dd>
                </>
              )}
              {state.contextTokens != null && state.contextWindow ? (
                <>
                  <dt title="How full the agent's working memory is. Near 100% it compacts (summarizes) older turns.">Context used</dt>
                  <dd>
                    <Meter value={state.contextTokens / state.contextWindow} />
                    {state.compactions > 0 && <span className="cc-stat-note">compacted {state.compactions}×</span>}
                  </dd>
                </>
              ) : null}
              {five && (
                <>
                  <dt>Plan usage, 5 hours</dt>
                  <dd>
                    <Meter value={five.utilization} warnAt={0.8} />
                    {resetIn(five.resetsAt) && <span className="cc-stat-note">{resetIn(five.resetsAt)}</span>}
                  </dd>
                </>
              )}
              {seven && (
                <>
                  <dt>Plan usage, 7 days</dt>
                  <dd>
                    <Meter value={seven.utilization} warnAt={0.8} />
                  </dd>
                </>
              )}
              <dt>Session cost</dt>
              <dd>
                {fmtCost(state.costUsd)}
                <span className="cc-stat-note">{fmtTokens(tokensUsed)} tokens</span>
              </dd>
            </dl>
            {items.map((m, i) => (
              <div key={m.label} className="cc-menu-item">
                {m.group && m.group !== items[i - 1]?.group && <div className="cc-menu-group">{m.group}</div>}
                <button
                  type="button"
                  role={m.group ? 'menuitemradio' : 'menuitem'}
                  aria-checked={m.group ? !!m.checked : undefined}
                  className={m.checked ? 'cc-menu-current' : undefined}
                  onClick={() => {
                    setMenu(false)
                    if (!m.checked) m.onSelect()
                  }}
                >
                  {m.tone && <span className={`cc-menu-dot cc-tone-${m.tone}`} />}
                  <span className="cc-menu-text">
                    <span>{m.label}</span>
                    {m.detail && <span className="cc-menu-detail">{m.detail}</span>}
                  </span>
                  {m.checked && <span className="cc-menu-check">current</span>}
                </button>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
  return slot ? createPortal(controls, slot) : controls
}
