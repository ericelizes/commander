// Rail is the thin column down the left of the window, on the background: the
// pages (Home, Progress), then how many agents are working, and at the bottom
// the health and update alerts, the Settings gear, and who the app is for.

import { Bot, ChartNoAxesColumn, House, Settings as Gear, User, Workflow } from 'lucide-react'
import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { useIntegrations } from '../../../features/linear/settings'
import { getSettings } from '../settingsApi'
import { useStore } from '../store'
import { homeActive } from '../viewmodel'
import { SettingsModal, useAgentRows } from './SettingsModal'
import { UpdateBanner } from './UpdateBanner'
import { HealthBadge } from './HealthBadge'

// The selected page's icon is the filled form of its outline.
function HomeFilled() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden>
      <path d="M10.6 2.9a2.1 2.1 0 0 1 2.8 0l7 6.2c.4.4.6.9.6 1.5V19a2.5 2.5 0 0 1-2.5 2.5H15a1 1 0 0 1-1-1V15a1 1 0 0 0-1-1h-2a1 1 0 0 0-1 1v5.5a1 1 0 0 1-1 1H5.500A2.500 2.500 0 0 1 3 19v-8.400c0-.6.2-1.100.6-1.500z" />
    </svg>
  )
}

function ProgressFilled() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="currentColor" aria-hidden>
      <rect x="2.750" y="13.500" width="4.500" height="8" rx="1.500" />
      <rect x="9.750" y="2.500" width="4.500" height="19" rx="1.500" />
      <rect x="16.750" y="8" width="4.500" height="13.500" rx="1.500" />
    </svg>
  )
}

function initials(name: string) {
  return name.split(/\s+/).filter(Boolean).slice(0, 2).map((word) => word[0].toUpperCase()).join('')
}

export function Rail() {
  const snapshot = useStore((s) => s.snapshot)
  const conn = useStore((s) => s.conn)
  const unknown = !!snapshot?.degraded || (snapshot?.storage === 'postgres' && conn !== 'live')
  const config = useStore((s) => s.config)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  useEffect(() => {
    useIntegrations.setState({ linearWorkspace: '', linearTeam: '', timezone: '' })
    if (!config) return
    let active = true
    void getSettings(config).then((s) => {
      if (!active || !s) return
      setName(s.user_name)
      useIntegrations.setState({ linearWorkspace: s.linear_workspace ?? '', linearTeam: s.linear_team ?? '', timezone: s.timezone ?? '' })
    }, () => {})
    return () => { active = false }
  }, [config])
  const working = useAgentRows().filter((r) => r.state === 'working').length
  const home = useStore((s) => homeActive(s.selected))
  const progress = useStore((s) => s.selected.kind === 'progress')
  const workflows = useStore((s) => s.selected.kind === 'workflows')
  return (
    <div className="rail">
      <button type="button" className={`rail-btn${home ? ' on' : ''}`} onClick={() => useStore.getState().goHome()} title="Home" aria-label="Home">
        {home ? <HomeFilled /> : <House size={18} />}
      </button>
      <button type="button" className={`rail-btn${progress ? ' on' : ''}`} onClick={() => useStore.getState().select({ kind: 'progress' })} title="Progress" aria-label="Progress">
        {progress ? <ProgressFilled /> : <ChartNoAxesColumn size={18} />}
      </button>
      <button type="button" className={`rail-btn${workflows ? ' on' : ''}`} onClick={() => useStore.getState().select({ kind: 'workflows' })} title="Workflows" aria-label="Workflows">
        <Workflow size={18} strokeWidth={workflows ? 2.4 : 2} />
      </button>
      <div className="rail-divider" />
      <span className={`rail-metric${!unknown && working > 0 ? ' is-live' : ''}`} title={unknown ? 'Live agent state unavailable' : `${working} agent${working === 1 ? '' : 's'} working now`}>
        <Bot size={18} />
        {unknown ? '?' : working}
      </span>
      <div className="rail-foot">
        <HealthBadge />
        <UpdateBanner />
        <button type="button" className={`rail-btn${open ? ' on' : ''}`} onClick={() => setOpen(true)} title="Settings" aria-label="Settings" aria-haspopup="dialog">
          <Gear size={16} />
        </button>
        <span className="rail-user" title={name} aria-label={name}>
          {initials(name) || <User size={14} />}
        </span>
      </div>
      {/* A portal: a glass backdrop-filter would otherwise contain the fixed backdrop. */}
      {open && createPortal(<SettingsModal onClose={() => setOpen(false)} />, document.body)}
    </div>
  )
}
