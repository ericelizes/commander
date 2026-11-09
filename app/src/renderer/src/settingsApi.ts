// cmdrd's settings: the user's display name and the runtime split
// ([runtime] codex_manager_share / codex_operator_share in config.toml), and
// the rebalance that applies the split. The demo fixture answers locally.

import type { HostConfig } from '../../shared/snapshot'

export interface Settings {
  linear_workspace?: string
  linear_team?: string
  user_name: string
  timezone?: string // [user] timezone (IANA); absent = the browser's
  host?: string
  codex_manager_share: number
  codex_operator_share: number
}

export interface RebalanceResult {
  ok: boolean
  stdout?: string
  stderr?: string
  error?: string
}

let demo: Settings = { user_name: 'Demo User', codex_manager_share: 0.25, codex_operator_share: 0.5 }

const auth = (cfg: HostConfig) => ({ Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' })

export async function getSettings(cfg: HostConfig): Promise<Settings | null> {
  if (cfg.demo) return { ...demo }
  const res = await fetch(`${cfg.apiUrl}/api/settings`, { headers: auth(cfg) })
  if (!res.ok) return null
  const body = (await res.json()) as { settings?: Settings }
  return body.settings ?? null
}

/** saveShares writes the Codex shares (0-1); it answers the saved settings or an error. */
export async function saveShares(cfg: HostConfig, shares: Partial<Pick<Settings, 'codex_manager_share' | 'codex_operator_share'>>): Promise<{ settings?: Settings; error?: string }> {
  if (cfg.demo) {
    demo = { ...demo, ...shares }
    return { settings: { ...demo } }
  }
  const res = await fetch(`${cfg.apiUrl}/api/settings/runtime`, { method: 'POST', headers: auth(cfg), body: JSON.stringify(shares) })
  const body = (await res.json().catch(() => ({}))) as { settings?: Settings; error?: string }
  return res.ok ? { settings: body.settings } : { error: body.error ?? `HTTP ${res.status}` }
}

export async function rebalance(cfg: HostConfig, dryRun: boolean): Promise<RebalanceResult> {
  if (cfg.demo) return { ok: true, stdout: dryRun ? 'would move 1 Manager and 2 Operators to Codex' : 'moved 1 Manager and 2 Operators to Codex' }
  const res = await fetch(`${cfg.apiUrl}/api/runtime/rebalance`, { method: 'POST', headers: auth(cfg), body: JSON.stringify({ dry_run: dryRun }) })
  const body = (await res.json().catch(() => ({}))) as RebalanceResult
  return { ...body, ok: res.ok && body.ok !== false }
}

/** AgentUsage is one agent's token use on one account and model (tokens include cache reads and writes). */
export interface AgentUsage {
  account: string
  runtime: string
  role: string
  model: string
  today_tokens: number
  week_tokens: number
  week_usd: number
  week_turns: number
}

/** AccountTokens is one account's token use and its top agents this week; key is claude:<account> or codex. */
export interface AccountTokens {
  key: string
  today_tokens: number
  week_tokens: number
  week_usd: number
  agents: AgentUsage[]
}

/** getAgentUsage answers which agents and models used each account's tokens (today and the last 7 days). */
export async function getAgentUsage(cfg: HostConfig): Promise<AccountTokens[]> {
  if (cfg.demo) return []
  const res = await fetch(`${cfg.apiUrl}/api/usage/agents`, { headers: auth(cfg) })
  if (!res.ok) return []
  const body = (await res.json().catch(() => ({}))) as { accounts?: AccountTokens[] }
  return body.accounts ?? []
}

export interface Machine {
  host: string
  os: string
  cpus: number
  load1: number
  mem_total_bytes: number
  mem_available_bytes: number
  disk_total_bytes: number
  disk_free_bytes: number
}

export interface MachineSample {
  at: number
  mem: number
  disk: number
  cpu: number
}

/** getMachine is how much room the computer cmdrd runs on has left, and the last 30 minutes of it. */
export async function getMachine(cfg: HostConfig): Promise<{ machine: Machine; history: MachineSample[] } | null> {
  if (cfg.demo) return null
  const res = await fetch(`${cfg.apiUrl}/api/machine`, { headers: auth(cfg) })
  if (!res.ok) return null
  const body = (await res.json()) as { machine?: Machine; history?: MachineSample[] }
  return body.machine ? { machine: body.machine, history: body.history ?? [] } : null
}
