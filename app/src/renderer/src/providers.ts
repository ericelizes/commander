// Providers: which Claude account (or Codex) an agent runs on, the "Switch to"
// list every chat menu, the usage-limit notice and the Settings agents table
// share, and how an account's plan usage reads. Pure: no fetches, no store.

import type { SnapExecution, SnapStep } from '../../shared/snapshot'
import type { ChatAccount, ChatRuntime, Provider, Usage } from './chatApi'

export type UsageState = 'ok' | 'warning' | 'out' | 'unknown'

/** SwitchTarget is one "Switch to" entry: a logged-in Claude account, or Codex. */
export interface SwitchTarget {
  key: string // "claude:<account>" | "codex"
  label: string // "Claude · default" | "Codex"
  verb: 'account' | 'runtime'
  body: { account: string } | { runtime: ChatRuntime }
  current: boolean
  state: UsageState
  detail: string // "5h 40% · 7d 12%", "out until 4:00pm", "no usage reported yet"
}

export function providerKey(p: Provider | undefined): string {
  if (p?.runtime === 'codex') return 'codex'
  return `claude:${p?.account || 'default'}`
}

export function providerLabel(p: Provider | undefined): string {
  if (p?.runtime === 'codex') return 'Codex'
  return `Claude · ${p?.account || 'default'}`
}

/** usageState is u's status as of now: an "out" whose reset passed is ok again. */
export function usageState(u: Usage | null | undefined, nowSec: number): UsageState {
  if (!u) return 'unknown'
  if (u.status === 'out' && u.out_until && nowSec >= u.out_until) return 'ok'
  return u.status
}

/** pct is a window's use as a whole percent, 0 once its reset passed. */
export function pct(w: { utilization: number; resets_at?: number } | undefined, nowSec: number): number | undefined {
  if (!w) return undefined
  if (w.resets_at && nowSec >= w.resets_at) return 0
  return Math.round(w.utilization * 100)
}

/** clock is "4:00pm" today, else "Mon 4:00pm". */
export function clock(sec: number, nowSec: number): string {
  const d = new Date(sec * 1000)
  const now = new Date(nowSec * 1000)
  let h = d.getHours()
  const ampm = h >= 12 ? 'pm' : 'am'
  h = h % 12 || 12
  const t = `${h}:${String(d.getMinutes()).padStart(2, '0')}${ampm}`
  return d.toDateString() === now.toDateString() ? t : `${d.toLocaleDateString('en-US', { weekday: 'short' })} ${t}`
}

/** usageDetail is the one-line usage of an account, for menus and the notice. */
export function usageDetail(u: Usage | null | undefined, nowSec: number): string {
  if (!u) return 'no usage reported yet'
  if (usageState(u, nowSec) === 'out') return u.out_until ? `out until ${clock(u.out_until, nowSec)}` : 'out of usage'
  const parts: string[] = []
  const five = pct(u.five_hour, nowSec)
  const seven = pct(u.seven_day, nowSec)
  if (five !== undefined) parts.push(`5h ${five}%`)
  if (seven !== undefined) parts.push(`7d ${seven}%`)
  return parts.join(' · ') || 'no usage reported yet'
}

/**
 * switchTargets is the "Switch to" list for an agent on cur: every logged-in
 * Claude account, then Codex, each with its usage; the one it is on is
 * current. Without an account list (an older cmdrd) it is Claude and Codex.
 */
export function switchTargets(accounts: ChatAccount[], cur: Provider | undefined, nowSec: number): SwitchTarget[] {
  const curKey = providerKey(cur)
  const list = accounts.length > 0 ? accounts : [{ name: 'default', runtime: 'claude' as const, logged_in: true }, { name: 'codex', runtime: 'codex' as const, logged_in: true }]
  const out: SwitchTarget[] = []
  for (const a of list) {
    if (!a.logged_in || a.duplicate_of) continue
    const p: Provider = a.runtime === 'codex' ? { runtime: 'codex' } : { runtime: 'claude', account: a.name }
    const key = providerKey(p)
    out.push({
      key,
      label: providerLabel(p),
      verb: a.runtime === 'codex' ? 'runtime' : 'account',
      body: a.runtime === 'codex' ? { runtime: 'codex' } : { account: a.name },
      current: key === curKey,
      state: usageState(a.usage, nowSec),
      detail: usageDetail(a.usage, nowSec),
    })
  }
  return out
}

/** AgentRow is one running agent in Settings, Agents. */
export interface AgentRow {
  role: string
  name: string
  kind: 'commander' | 'manager' | 'operator'
  workstream: string // "" for Commander
  wsKey?: string // its workstream's navigator key
  provider: Provider
  state: string // working / idle / waiting / stuck / ...
  since?: number // unix seconds of its last state change
}

/** The fields agentRows reads (subsets of the snapshot and chat statuses). */
export interface AgentInputs {
  executions?: SnapExecution[] | null
  agents: Record<string, { state: string; since: number }>
  workstreams: { title: string; manager_role: string; project: string; bead_id: string; project_id?: number; steps?: SnapStep[] }[]
  operators: { role: string; label: string; task?: string; wsTitle: string; wsKey: string; state: string }[]
  // watched is each running Operator process by its Manager (snapshot.watch), the
  // only record of a tmux-hosted Operator being alive.
  watched: { role: string; manager: string; task?: string }[]
  chats: Record<string, { running: boolean; state: string; runtime?: ChatRuntime; account?: string }>
  providers: Record<string, Provider>
}

/**
 * agentRows lists the agents whose process is running now: Commander and
 * Managers by their chat, Operators by their chat or the watch. A stopped chat
 * or a Manager's recorded state is not a running agent. Its provider is the running chat's,
 * else the recorded one, else Claude on default.
 */
export function agentRows(x: AgentInputs): AgentRow[] {
  const providerOf = (role: string): Provider => {
    const c = x.chats[role]
    if (c?.running && c.runtime) return c.runtime === 'codex' ? { runtime: 'codex' } : { runtime: 'claude', account: c.account || 'default' }
    const p = x.providers[role]
    if (p) return p.runtime === 'codex' ? { runtime: 'codex' } : { runtime: 'claude', account: p.account || 'default' }
    return { runtime: 'claude', account: 'default' }
  }
  const stateOf = (role: string) => (x.chats[role]?.running ? x.chats[role].state : x.agents[role]?.state) ?? ''
  const live = (role: string) => !!x.chats[role]?.running
  const watched = new Set(x.watched.map((w) => w.role))
  if (x.executions != null) {
    const descendants = (steps: SnapStep[], id: string): SnapStep | undefined => {
      for (const st of steps) { if (st.bead_id === id) return st; const found = descendants(st.children ?? [], id); if (found) return found }
    }
    return x.executions.map((run): AgentRow => {
      const op = x.operators.find((o) => o.role.replaceAll('.', '-') === run.role.replaceAll('.', '-'))
      const ws = x.workstreams.find((w) => (w.project_id == null || w.project_id === run.project_id) && (w.bead_id === run.bead_id || descendants(w.steps ?? [], run.bead_id))) ?? x.workstreams.find((w) => `${w.project}:${w.bead_id}` === op?.wsKey)
      const step = ws && descendants(ws.steps ?? [], run.bead_id)
      const kind = run.role === 'commander' ? 'commander' : run.role.startsWith('manager-') ? 'manager' : 'operator'
      return { role: run.role, name: kind === 'commander' ? 'Commander' : step?.title ?? op?.task ?? ws?.title ?? run.role, kind, workstream: ws?.title ?? '', wsKey: ws ? `${ws.project}:${ws.bead_id}` : undefined, provider: providerOf(run.role), state: run.state, since: x.agents[run.role]?.since }
    })
  }
  const rows: AgentRow[] = []
  if (live('commander')) rows.push({ role: 'commander', name: 'Commander', kind: 'commander', workstream: '', provider: providerOf('commander'), state: stateOf('commander'), since: x.agents.commander?.since })
  for (const w of x.workstreams) {
    if (!live(w.manager_role)) continue
    rows.push({ role: w.manager_role, name: w.title, kind: 'manager', workstream: w.title, wsKey: `${w.project}:${w.bead_id}`, provider: providerOf(w.manager_role), state: stateOf(w.manager_role), since: x.agents[w.manager_role]?.since })
  }
  const seen = new Set<string>()
  for (const o of x.operators) {
    if (!live(o.role) && !watched.has(o.role)) continue
    seen.add(o.role)
    rows.push({ role: o.role, name: o.task && o.task !== o.wsTitle ? o.task : o.label, kind: 'operator', workstream: o.wsTitle, wsKey: o.wsKey, provider: providerOf(o.role), state: o.state === 'tmux' ? 'working' : o.state, since: x.agents[o.role]?.since })
  }
  for (const w of x.watched) {
    if (seen.has(w.role)) continue
    seen.add(w.role)
    const ws = x.workstreams.find((s) => s.manager_role === w.manager)
    rows.push({ role: w.role, name: w.task ?? w.role.replace(/^operator-(op-)?/, ''), kind: 'operator', workstream: ws?.title ?? '', wsKey: ws ? `${ws.project}:${ws.bead_id}` : undefined, provider: providerOf(w.role), state: 'working' })
  }
  return rows
}

export type GroupBy = 'provider' | 'account' | 'workstream'

/** groupRows groups rows (provider: Claude vs Codex; account: each Claude account and Codex). */
export function groupRows(rows: AgentRow[], by: GroupBy): { key: string; label: string; rows: AgentRow[] }[] {
  const keyOf = (r: AgentRow): [string, string] => {
    if (by === 'workstream') return [r.workstream || 'Commander', r.workstream || 'Commander']
    if (by === 'provider') return r.provider.runtime === 'codex' ? ['codex', 'Codex'] : ['claude', 'Claude']
    return [providerKey(r.provider), providerLabel(r.provider)]
  }
  const groups = new Map<string, { key: string; label: string; rows: AgentRow[] }>()
  for (const r of rows) {
    const [key, label] = keyOf(r)
    if (!groups.has(key)) groups.set(key, { key, label, rows: [] })
    groups.get(key)!.rows.push(r)
  }
  return [...groups.values()]
}

/** since is "4m", "2h 5m", "3d" ago. */
export function ago(sec: number | undefined, nowSec: number): string {
  if (!sec) return ''
  const d = Math.max(0, nowSec - sec)
  if (d < 3600) return `${Math.max(1, Math.round(d / 60))}m`
  if (d < 86400) return `${Math.floor(d / 3600)}h ${Math.round((d % 3600) / 60)}m`
  return `${Math.floor(d / 86400)}d`
}
