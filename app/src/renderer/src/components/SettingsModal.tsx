// Settings: the one place for app-level controls, opened from the gear in the
// rail. Connections (the side panel's site sign-ins), Agents
// (every running agent and what it runs on, each account's plan usage, adding
// an account, the Codex share of Managers and Operators), Machine (memory, disk
// and CPU left on the computer Commander runs on) and Display (theme).

import { LinearSettings } from '../../../features/linear/LinearSettings'
import { Cloud } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { hasConnections, openInPanel, signinStatus, SIGNIN_SITES } from '../../../features/inspector'
import { agentRows, type AgentRow } from '../providers'
import { getMachine, getSettings, rebalance, saveShares, type Machine, type MachineSample, type Settings } from '../settingsApi'
import { useStore } from '../store'
import { operatorsVM } from '../viewmodel'
import type { SnapStep } from '../../../shared/snapshot'
import { blockText, invalidBlockLines, setBlockText } from '../pageCapture'
import { MachineChart } from './MachineChart'
import { ThemeSwitch } from './ThemeSwitch'
import { UsageTab } from './UsageTab'

export type SettingsSection = 'connections' | 'agents' | 'machine' | 'privacy' | 'display'

const SECTIONS: { id: SettingsSection; label: string }[] = [
  { id: 'connections', label: 'Connections' },
  { id: 'agents', label: 'Agents' },
  { id: 'machine', label: 'Machine' },
  { id: 'privacy', label: 'Blocked sites' },
  { id: 'display', label: 'Display' },
]

export function SettingsModal({ onClose, section: initial = 'agents', onSaved }: { onClose: () => void; section?: SettingsSection; onSaved?: (s: Settings) => void }) {
  const [section, setSection] = useState<SettingsSection>(initial)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal settings-modal" role="dialog" aria-modal="true" aria-label="Settings">
        <nav className="settings-nav" aria-label="Settings sections">
          <h3>Settings</h3>
          {SECTIONS.map((s) => (
            <button key={s.id} type="button" className={section === s.id ? 'on' : ''} aria-current={section === s.id ? 'page' : undefined} onClick={() => setSection(s.id)}>
              {s.label}
            </button>
          ))}
        </nav>
        <div className="settings-body">
          <button type="button" className="settings-close" aria-label="Close settings" onClick={onClose}>
            ×
          </button>
          {section === 'connections' && <><LinearSettings /><Connections onDone={onClose} /></>}
          {section === 'agents' && <AgentsSection onSaved={onSaved} />}
          {section === 'machine' && <MachineSection />}
          {section === 'privacy' && <CaptureSection />}
          {section === 'display' && (
            <>
              <h4>Display</h4>
              <div className="settings-row">
                <span>Theme</span>
                <ThemeSwitch />
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// CaptureSection: the pages never attached to a message (see pageCapture.ts).
function CaptureSection() {
  const [text, setText] = useState(blockText)
  const bad = invalidBlockLines()
  return (
    <>
      <h4>Blocked sites</h4>
      <p className="settings-note">
        Share focused window, in the + menu of a chat, sends a screenshot and the text of the web tab you are looking at with each message. Nothing is ever shared from a site listed here.
      </p>
      <p className="settings-note">
        Nothing is attached from a page that matches a line below. Block the whole site (<code>private.example.com</code>, which covers its subdomains and ports); a path
        (<code>host/path</code>) is best effort, since a site may show the same page under another path. Add every site with sensitive data.
      </p>
      <textarea
        className="settings-block"
        aria-label="Pages never attached"
        rows={6}
        spellCheck={false}
        placeholder={'one per line, e.g.\nprivate.example.com\n*.records.example.org\ngithub.com/my-org/private'}
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setBlockText(e.target.value)
        }}
      />
      {bad.length > 0 && (
        <p className="settings-note" role="alert">
          Cannot read: {bad.join(', ')}. Until fixed, nothing is attached.
        </p>
      )}
    </>
  )
}

function MachineSection() {
  const config = useStore((s) => s.config)
  const [m, setM] = useState<Machine | null>(null)
  const [history, setHistory] = useState<MachineSample[]>([])
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!config) return
    const load = () =>
      getMachine(config).then(
        (x) => {
          setM(x?.machine ?? null)
          setHistory(x?.history ?? [])
          setFailed(!x)
        },
        () => setFailed(true),
      )
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  }, [config])
  const remote = !!m && !!config?.localHost && m.host !== config.localHost
  const now = history.length ? history[history.length - 1].at : Math.floor(Date.now() / 1000)
  const series = (k: 'mem' | 'disk' | 'cpu') => history.map((h) => ({ at: h.at, pct: h[k] }))
  return (
    <>
      <h4>Machine</h4>
      <p className="settings-sub">
        {remote && <Cloud className="settings-remote" size={13} strokeWidth={2.2} aria-hidden />}
        {m ? `${remote ? 'Remote: ' : ''}${m.host}, the computer Commander and its agents run on.` : failed ? 'Commander did not report its machine.' : 'Reading…'}
      </p>
      {m && (
        <div className="machine-charts">
          <MachineChart name="Memory" points={series('mem')} now={now} detail={`${fmtBytes(m.mem_available_bytes)} free of ${fmtBytes(m.mem_total_bytes)}`} />
          <MachineChart name="CPU" points={series('cpu')} now={now} detail={`load ${m.load1.toFixed(1)} on ${m.cpus} cores`} />
          <MachineChart name="Disk" points={series('disk')} now={now} detail={`${fmtBytes(m.disk_free_bytes)} free of ${fmtBytes(m.disk_total_bytes)}`} />
        </div>
      )}
    </>
  )
}

/** fmtBytes is a size at a glance: 512 MB, 7.8 GB, 1.2 TB. */
export function fmtBytes(n: number): string {
  const gb = n / 1024 ** 3
  if (gb >= 1024) return `${(gb / 1024).toFixed(1)} TB`
  if (gb >= 1) return `${gb.toFixed(gb < 10 ? 1 : 0)} GB`
  return `${Math.round(n / 1024 ** 2)} MB`
}

function Connections({ onDone }: { onDone: () => void }) {
  const [signedIn, setSignedIn] = useState<Record<string, boolean>>({})
  const available = hasConnections()
  useEffect(() => {
    if (!available) return
    const load = () => signinStatus().then(setSignedIn, () => {})
    load()
    const t = setInterval(load, 3000)
    return () => clearInterval(t)
  }, [available])
  return (
    <>
      <h4>Connections</h4>
      <p className="settings-sub">{available ? 'Sign in once; pages in the side panel stay signed in.' : 'Available in the desktop app.'}</p>
      {available &&
        SIGNIN_SITES.map((s) => (
          <div key={s.id} className="settings-row">
            <span>{s.name}</span>
            {signedIn[s.id] ? (
              <span className="settings-ok">Connected</span>
            ) : (
              <button
                type="button"
                className="side-pop-btn"
                onClick={() => {
                  openInPanel(s.loginUrl, `Sign in: ${s.name}`)
                  onDone()
                }}
              >
                Sign in
              </button>
            )}
          </div>
        ))}
    </>
  )
}

export function useAgentRows(): AgentRow[] {
  const snapshot = useStore((s) => s.snapshot)
  const chats = useStore((s) => s.chats)
  const providers = useStore((s) => s.providers)
  return useMemo(
    () =>
      snapshot
        ? agentRows({
            agents: snapshot.agents ?? {},
            workstreams: snapshot.workstreams,
            operators: operatorsVM(snapshot, chats),
            watched: Object.entries(snapshot.watch ?? {}).flatMap(([manager, w]) => (w.operators ?? []).map((o) => ({ role: o.role, manager, task: stepTitle(snapshot.workstreams, o.bead_id) }))),
            chats,
            providers,
          })
        : [],
    [snapshot, chats, providers],
  )
}

// stepTitle is the title of the step with this bead id, anywhere in any workstream.
function stepTitle(workstreams: { steps?: SnapStep[] }[], beadID: string): string | undefined {
  const find = (steps: SnapStep[]): SnapStep | undefined => {
    for (const st of steps) {
      if (st.bead_id === beadID) return st
      const hit = st.children && find(st.children)
      if (hit) return hit
    }
  }
  for (const w of workstreams) {
    const hit = find(w.steps ?? [])
    if (hit) return hit.title
  }
}

function AgentsSection({ onSaved }: { onSaved?: (s: Settings) => void }) {
  const [tab, setTab] = useState<'agents' | 'usage'>('agents')
  const rows = useAgentRows()
  return (
    <>
      <h4>Agents</h4>
      <div className="seg" role="tablist" aria-label="Agents view">
        <button type="button" role="tab" aria-selected={tab === 'agents'} onClick={() => setTab('agents')}>
          Agents
        </button>
        <button type="button" role="tab" aria-selected={tab === 'usage'} onClick={() => setTab('usage')}>
          Usage
        </button>
      </div>
      {tab === 'agents' ? (
        <>
          <AgentsTab />
          <RuntimeSplit onSaved={onSaved} />
        </>
      ) : (
        <UsageTab rows={rows} />
      )}
    </>
  )
}

const KINDS: { kind: AgentRow['kind']; label: string }[] = [
  { kind: 'commander', label: 'Commander' },
  { kind: 'manager', label: 'Managers' },
  { kind: 'operator', label: 'Operators' },
]
const RUNTIMES = ['claude', 'codex'] as const

// AgentsTab counts the running agents: Commander, Managers and Operators by
// provider, working vs idle.
function AgentsTab() {
  const rows = useAgentRows()
  const cell = (kind: AgentRow['kind'], runtime?: string) => {
    const in_ = rows.filter((r) => r.kind === kind && (!runtime || r.provider.runtime === runtime))
    const working = in_.filter((r) => r.state === 'working').length
    return { working, idle: in_.length - working }
  }
  const show = (c: { working: number; idle: number }) =>
    c.working + c.idle === 0 ? (
      <span className="agents-none">—</span>
    ) : (
      <>
        <b className="agents-working">{c.working}</b> working <span className="agents-idle">· {c.idle} idle</span>
      </>
    )
  const working = rows.filter((r) => r.state === 'working').length
  return (
    <div role="tabpanel" aria-label="Agents">
      <div className="agents-tiles">
        <div className="agents-tile tile-working">
          <b>{working}</b>
          <span>working</span>
        </div>
        <div className="agents-tile">
          <b>{rows.length - working}</b>
          <span>idle</span>
        </div>
        <div className="agents-tile">
          <b>{rows.length}</b>
          <span>running</span>
        </div>
      </div>
      <table className="agents-table">
        <thead>
          <tr>
            <th />
            <th>Claude</th>
            <th>Codex</th>
            <th>All</th>
          </tr>
        </thead>
        <tbody>
          {KINDS.map(({ kind, label }) => (
            <tr key={kind}>
              <th scope="row">{label}</th>
              {RUNTIMES.map((rt) => (
                <td key={rt}>{show(cell(kind, rt))}</td>
              ))}
              <td>{show(cell(kind))}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="settings-sub">Switch an agent's provider from its chat's ⋯ menu.</p>
    </div>
  )
}

function RuntimeSplit({ onSaved }: { onSaved?: (s: Settings) => void }) {
  const config = useStore((s) => s.config)
  const [mgr, setMgr] = useState('')
  const [ops, setOps] = useState('')
  const [out, setOut] = useState('')
  const [error, setError] = useState('')
  useEffect(() => {
    if (!config) return
    void getSettings(config).then((s) => {
      if (!s) return
      setMgr(String(Math.round(s.codex_manager_share * 100)))
      setOps(String(Math.round(s.codex_operator_share * 100)))
    })
  }, [config])
  if (!config) return null
  const save = async () => {
    setError('')
    const m = Number(mgr) / 100
    const o = Number(ops) / 100
    if (![m, o].every((v) => Number.isFinite(v) && v >= 0 && v <= 1)) {
      setError('Shares are 0 to 100%.')
      return
    }
    const r = await saveShares(config, { codex_manager_share: m, codex_operator_share: o })
    if (r.error) setError(r.error)
    else if (r.settings) {
      setOut('Saved.')
      onSaved?.(r.settings)
    }
  }
  const run = async (dry: boolean) => {
    setError('')
    const r = await rebalance(config, dry)
    if (!r.ok) setError((r.stderr || r.error || 'rebalance failed').trim())
    else setOut((r.stdout || 'Done.').trim())
  }
  return (
    <section className="runtime-split" aria-label="Runtime split">
      <div className="agents-group-head">Runtime split</div>
      <p className="settings-sub">The share of agents that run on Codex; the rest run on Claude. Rebalance moves agents between turns to match.</p>
      <div className="split-row">
        <label>
          Managers on Codex
          <input type="number" min={0} max={100} step={5} value={mgr} onChange={(e) => setMgr(e.target.value)} aria-label="Managers on Codex (%)" />%
        </label>
        <label>
          Operators on Codex
          <input type="number" min={0} max={100} step={5} value={ops} onChange={(e) => setOps(e.target.value)} aria-label="Operators on Codex (%)" />%
        </label>
        <button type="button" className="btn" onClick={() => void save()}>
          Save
        </button>
        <button type="button" className="btn" onClick={() => void run(true)}>
          Preview
        </button>
        <button type="button" className="btn btn-primary" onClick={() => void run(false)}>
          Rebalance now
        </button>
      </div>
      {out && <pre className="split-out">{out}</pre>}
      {error && (
        <p className="state-error" role="alert">
          {error}
        </p>
      )}
    </section>
  )
}
