// The app's view of cmdrd's native chat engine: the session list (polled for
// the navigator and the middle header), the per-role WebSocket transport, and
// the explicit, user-confirmed lifecycle calls (start, handover).
// Nothing here starts or hands over a chat on its own.

import { useEffect } from 'react'
import { WsChatTransport, type ChatTransport } from '../../features/chat'
import type { HostConfig } from '../../shared/snapshot'
import { demoChat } from './demoChat'
import { useStore } from './store'

export interface ChatStatus {
  role: string
  running: boolean
  session_id?: string // current resolved session, including before its first native start
  state: string
  detail?: string | null
  model?: string | null
  last_seq: number
  total_cost_usd: number
  pending: unknown[]
  owned_by?: string // not running here because another process (e.g. a tmux TUI) holds the session
  owner_pid?: number
  runtime?: ChatRuntime // the agent CLI it runs (or will run) on
  account?: string // the Claude account it runs (or will run) on
  auto_switch?: boolean
  usage_limit?: UsageLimit | null // the last turn stopped at a plan limit
}

export type ChatRuntime = 'claude' | 'codex'

export type ChatVerb = 'start' | 'handover' | 'runtime' | 'account' | 'fresh'

/** UsageLimit: a Claude (or Codex) plan limit a chat's turn stopped at. */
export interface UsageLimit {
  text: string
  resets_at?: number // unix seconds
  runtime: ChatRuntime
  account?: string
  at: string
}

/** UsageWindow: share used (0-1) of a 5-hour or 7-day plan window. */
export interface UsageWindow {
  utilization: number
  resets_at?: number
}

/** Usage: the latest plan usage an account reported (from its chats' rate-limit events). */
export interface Usage {
  runtime: ChatRuntime
  account?: string
  status: 'ok' | 'warning' | 'out'
  five_hour?: UsageWindow
  seven_day?: UsageWindow
  out_until?: number
  role: string
  at: string
}

/** ChatAccount: a Claude account (by name) or Codex (name "codex"). */
export interface ChatAccount {
  name: string
  runtime: ChatRuntime
  logged_in: boolean
  email?: string
  account_uuid?: string
  duplicate_of?: string
  dir?: string
  usage?: Usage | null
}

/** Provider: what a role runs on. */
export interface Provider {
  runtime: ChatRuntime
  account?: string
}

export interface ChatCallResult {
  ok: boolean
  code: number
  error?: string
  owned_by?: string // 409: who owns the session ("terminal", "headless", "engine", "lock")
  pid?: number
}

export interface ChatBackend {
  transport: ChatTransport
  list(): Promise<ChatStatus[] | null> // null: no engine on this cmdrd (404/503)
  call(role: string, verb: ChatVerb, body?: Record<string, unknown>): Promise<ChatCallResult>
  /** The accounts and per-role providers the last list() carried. */
  providers?(): { accounts: ChatAccount[]; providers: Record<string, Provider> }
  /** Create a Claude account and open its login in a terminal (the user logs in there). */
  addAccount?(name: string): Promise<AccountLoginResult>
  /** Open an existing account's login terminal again. */
  startSignin?(name: string): Promise<SigninStart>
  sendSigninCode?(name: string, code: string): Promise<{ ok: boolean; error?: string; email?: string }>
  signinState?(name: string): Promise<'none' | 'waiting' | 'done' | 'failed'>
  cancelSignin?(name: string): Promise<void>
  signOut?(name: string): Promise<{ ok: boolean; error?: string }>
}

export interface AccountLoginResult {
  ok: boolean
  error?: string
  account?: string
  before?: ChatAccount
  login_command?: string
  opened?: boolean
}

/** SigninStart is a sign-in underway: where to go, and Codex's device code to enter there. */
export interface SigninStart {
  ok: boolean
  error?: string
  url?: string
  user_code?: string
}

export function cmdrdChat(cfg: HostConfig): ChatBackend {
  const headers = { Authorization: `Bearer ${cfg.token}` }
  let last: { accounts: ChatAccount[]; providers: Record<string, Provider> } = { accounts: [], providers: {} }
  const login = async <T extends { ok: boolean; error?: string } = AccountLoginResult>(path: string, body?: unknown): Promise<T> => {
    const res = await fetch(`${cfg.apiUrl}${path}`, {
      method: 'POST',
      headers: { ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify(body ?? {}),
    })
    const j = (await res.json().catch(() => ({}))) as T
    return { ...j, ok: res.ok && j.ok !== false, error: j.error ?? (res.ok ? undefined : `HTTP ${res.status}`) }
  }
  return {
    transport: new WsChatTransport({ baseUrl: cfg.apiUrl, token: cfg.token }),
    async list() {
      const res = await fetch(`${cfg.apiUrl}/api/chat`, { headers })
      if (res.status === 404 || res.status === 503) return null
      if (!res.ok) throw new Error(`chat list: HTTP ${res.status}`)
      const body = (await res.json()) as { sessions?: ChatStatus[]; accounts?: ChatAccount[]; providers?: Record<string, Provider> }
      last = { accounts: body.accounts ?? [], providers: body.providers ?? {} }
      return body.sessions ?? []
    },
    providers: () => last,
    addAccount: (name) => login('/api/chat/accounts', { name }),
    startSignin: (name) => login<SigninStart>(`/api/chat/accounts/${encodeURIComponent(name)}/signin`),
    sendSigninCode: (name, code) => login<{ ok: boolean; error?: string; email?: string }>(`/api/chat/accounts/${encodeURIComponent(name)}/signin/code`, { code }),
    async signinState(name) {
      const res = await fetch(`${cfg.apiUrl}/api/chat/accounts/${encodeURIComponent(name)}/signin`, { headers })
      return ((await res.json()) as { state: 'none' | 'waiting' | 'done' | 'failed' }).state
    },
    async cancelSignin(name) {
      await fetch(`${cfg.apiUrl}/api/chat/accounts/${encodeURIComponent(name)}/signin`, { method: 'DELETE', headers })
    },
    signOut: (name) => login<{ ok: boolean; error?: string }>(`/api/chat/accounts/${encodeURIComponent(name)}/signout`),
    async call(role, verb, extra) {
      // handover replaces a tmux pane: cmdrd requires the explicit confirm.
      const body = verb === 'handover' ? { confirm: true } : (extra ?? {})
      const res = await fetch(`${cfg.apiUrl}/api/chat/${encodeURIComponent(role)}/${verb}`, {
        method: 'POST',
        headers: { ...headers, 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const j = (await res.json().catch(() => ({}))) as Omit<ChatCallResult, 'code'>
      return { ...j, ok: res.ok && j.ok !== false, code: res.status, error: res.ok ? undefined : (j.error ?? `${verb}: HTTP ${res.status}`) }
    },
  }
}

const backends = new WeakMap<HostConfig, ChatBackend>()

/** chatBackend is memoized per config, so the transport (and pane subscriptions) stay put. */
export function chatBackend(cfg: HostConfig): ChatBackend {
  let b = backends.get(cfg)
  if (!b) {
    b = cfg.demo ? demoChat() : cmdrdChat(cfg)
    backends.set(cfg, b)
  }
  return b
}

const POLL_MS = 3000

let listing = false

/** refreshChats reloads the session list into the store now. */
export async function refreshChats(): Promise<void> {
  const cfg = useStore.getState().config
  // One list call at a time: when cmdrd is slow they stack up, fill the
  // browser's six connections to it, and a chat send waits behind them.
  if (!cfg || listing) return
  listing = true
  try {
    const list = await chatBackend(cfg).list()
    if (useStore.getState().config !== cfg) return
    if (list === null) useStore.setState({ chatEngine: 'off', chats: {} })
    else {
      const p = chatBackend(cfg).providers?.()
      useStore.setState({
        chatEngine: 'on',
        chats: Object.fromEntries(list.map((s) => [s.role, s])),
        ...(p ? { accounts: p.accounts, providers: p.providers } : {}),
      })
    }
  } catch {
    // cmdrd unreachable (starting, restarting, offline): keep the last list and
    // keep checking. Only a 404/503 answer means this cmdrd has no chat engine.
  } finally {
    listing = false
  }
}

/** useChatPoll keeps the store's chat list fresh while the app is mounted. */
export function useChatPoll(): void {
  const config = useStore((s) => s.config)
  useEffect(() => {
    if (!config) return
    void refreshChats()
    const t = setInterval(() => void refreshChats(), POLL_MS)
    return () => clearInterval(t)
  }, [config])
}
