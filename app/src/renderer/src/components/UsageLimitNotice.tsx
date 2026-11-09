// The notice under a chat whose last turn stopped at a plan usage limit: one
// click moves it to another logged-in account (or Codex) with its usage shown,
// and the agent continues the same conversation there.

import type { UsageLimit } from '../chatApi'
import type { SwitchTarget } from '../providers'

export function UsageLimitNotice({
  limit,
  targets,
  busy,
  onSwitch,
}: {
  limit: UsageLimit
  targets: SwitchTarget[]
  busy: boolean
  onSwitch: (t: SwitchTarget) => void
}) {
  const who = limit.runtime === 'codex' ? 'Codex' : `Claude · ${limit.account || 'default'}`
  return (
    <div className="usage-notice" role="alert" aria-label="Usage limit">
      <div className="usage-notice-head">
        <span className="usage-dot usage-out" />
        <b>{who} is out of usage</b>
        <span className="usage-notice-text">{limit.text}</span>
      </div>
      {targets.length === 0 ? (
        <div className="usage-notice-sub">No other account is connected. Add one in Settings, Agents, Usage.</div>
      ) : (
        <div className="usage-notice-actions">
          <span className="usage-notice-sub">Continue on</span>
          {targets.map((t) => (
            <button key={t.key} type="button" className="usage-switch" disabled={busy || t.state === 'out'} onClick={() => onSwitch(t)} title={t.detail}>
              <span className={`usage-dot usage-${t.state}`} />
              <span>{t.label}</span>
              <span className="usage-switch-detail">{t.detail}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
