// A fake chat engine for `?fixture=chat` screenshots (no cmdrd). Conversations
// are cmdrd wire frames run through the real EngineMapper. Roles follow
// test/fixtures/snapshot.json:
//   manager-example-app-t03 (ws 1)  running, with history
//   manager-example-app-t01 (ws 2)  not running; Start answers 409 owned_by terminal
//   manager-example-app-t02 (ws 3)  stopped, with history (start card over it)
//   manager-example-app-t06 (ws 4)  running; its Operator op-exp-3 runs natively too
//   commander                     live in its tmux TUI (owned_by terminal): Move to app, never Start

import { EngineMapper, ReplayTransport, type ChatEvent, type ChatTransport } from '../../features/chat'
import { apiRetryFrames, managerAwayConversation, managerConversation } from '../../features/chat/engineFixtures'
import type { ChatAccount, ChatBackend, ChatStatus, Provider } from './chatApi'

const ZIM_OP = 'operator-op-exp-3-sample-example-app-t06-1-1'

const soon = (hours: number) => Math.floor(Date.now() / 1000) + hours * 3600

function status(role: string, running: boolean, last: number, cost: number, state = 'idle'): ChatStatus {
  return { role, running, state, last_seq: last, total_cost_usd: cost, pending: [], model: running ? 'claude-sonnet-5-5' : null }
}

export function demoChat(): ChatBackend {
  const sessions: Record<string, ChatStatus> = {
    'manager-example-app-t03': status('manager-example-app-t03', true, 19, 3.5982),
    'manager-example-app-t01': status('manager-example-app-t01', false, 0, 0),
    'manager-example-app-t02': status('manager-example-app-t02', false, 19, 2.1305),
    'manager-example-app-t06': status('manager-example-app-t06', true, 19, 0.6298),
    [ZIM_OP]: status(ZIM_OP, true, 19, 0.4102, 'working'),
    commander: { ...status('commander', false, 0, 0), owned_by: 'terminal', owner_pid: 25412 },
  }
  // t06's Manager just hit its plan limit: the notice offers the other accounts.
  sessions['manager-example-app-t06'].usage_limit = { text: "You've hit your limit · resets 4pm", runtime: 'claude', account: 'default', at: new Date().toISOString(), resets_at: soon(2) }
  const owned = new Set(['manager-example-app-t01'])
  const now = new Date().toISOString()
  const accounts: ChatAccount[] = [
    { name: 'default', runtime: 'claude', logged_in: true, email: 'me@example.com', usage: { runtime: 'claude', account: 'default', status: 'out', out_until: soon(2), five_hour: { utilization: 1, resets_at: soon(2) }, seven_day: { utilization: 0.41, resets_at: soon(90) }, role: 'manager-example-app-t06', at: now } },
    { name: 'second', runtime: 'claude', logged_in: true, email: 'second@example.com', usage: { runtime: 'claude', account: 'second', status: 'warning', five_hour: { utilization: 0.83, resets_at: soon(3) }, seven_day: { utilization: 0.22, resets_at: soon(100) }, role: 'commander', at: now } },
    { name: 'spare', runtime: 'claude', logged_in: false, email: 'spare@example.com' },
    { name: 'codex', runtime: 'codex', logged_in: true, email: 'codex@example.test', usage: { runtime: 'codex', status: 'ok', five_hour: { utilization: 0.12, resets_at: soon(4) }, seven_day: { utilization: 0.05, resets_at: soon(120) }, role: ZIM_OP, at: now } },
  ]
  const providers: Record<string, Provider> = { [ZIM_OP]: { runtime: 'codex' }, 'manager-example-app-t03': { runtime: 'claude', account: 'second' } }
  sessions['manager-example-app-t03'].account = 'second'
  sessions[ZIM_OP].runtime = 'codex'
  sessions[ZIM_OP].model = 'codex:gpt-6.1-sol'
  // The t06 Manager is mid-turn in API retries (the stall hint shows after 2 min).
  const history = (role: string): ChatEvent[] => {
    if (!sessions[role]?.last_seq) return []
    const frames = (new URLSearchParams(location.search).has('away') ? managerAwayConversation : managerConversation)(role)
    return new EngineMapper().map(role === 'manager-example-app-t06' ? [...frames, ...apiRetryFrames(role, 19, Date.now(), 150_000)] : frames, role)
  }
  const transports = new Map<string, ReplayTransport>()
  const transportFor = (role: string) => {
    let t = transports.get(role)
    if (!t) transports.set(role, (t = new ReplayTransport(history(role), { echo: true })))
    return t
  }
  const transport: ChatTransport = {
    subscribe: (role, since, onEvent, onConn) => transportFor(role).subscribe(role, since, onEvent, onConn),
    // Like cmdrd: a role with no native process gets the message by mail (no echo).
    send: async (role, req) => {
      if (!sessions[role]?.running) return { delivered: 'mail' as const }
      const t = transportFor(role)
      await t.send(role, req)
      streamReply(t)
    },
    interrupt: (role) => transportFor(role).interrupt(role),
    approve: (role, req) => transportFor(role).approve(role, req),
    answer: (role, req) => transportFor(role).answer(role, req),
  }
  return {
    transport,
    list: async () => Object.values(sessions).map((s) => ({ ...s })),
    providers: () => ({ accounts, providers }),
    addAccount: async (name) => {
      accounts.splice(accounts.length - 1, 0, { name, runtime: 'claude', logged_in: false })
      return { ok: true, account: name, opened: true, login_command: `CLAUDE_CONFIG_DIR=~/.commander/claude-accounts/${name} claude auth login` }
    },
    async call(role, verb, body) {
      const s = (sessions[role] ??= status(role, false, 0, 0))
      if (verb === 'account') {
        s.runtime = body?.account === 'codex' ? 'codex' : 'claude'
        s.account = s.runtime === 'codex' ? undefined : String(body?.account ?? 'default')
        s.usage_limit = null
        providers[role] = s.runtime === 'codex' ? { runtime: 'codex' } : { runtime: 'claude', account: s.account }
        s.model = s.runtime === 'codex' ? 'codex:gpt-6.1-sol' : 'claude-sonnet-5-5'
        if (s.running) transportFor(role).emit({ kind: 'init', model: s.model })
        return { ok: true, code: 200 }
      }
      if (verb === 'runtime') {
        s.runtime = body?.runtime === 'codex' ? 'codex' : 'claude'
        s.usage_limit = null
        providers[role] = s.runtime === 'codex' ? { runtime: 'codex' } : { runtime: 'claude', account: s.account }
        s.model = s.runtime === 'codex' ? 'codex:gpt-6.1-sol' : 'claude-sonnet-5-5'
        if (s.running) transportFor(role).emit({ kind: 'init', model: s.model }) // the restarted process's session event
        return { ok: true, code: 200 }
      }
      if (verb === 'start' && owned.has(role)) return { ok: false, code: 409, error: 'session owned by terminal (pid 48211)', owned_by: 'terminal', pid: 48211 }
      if (verb === 'handover') {
        delete s.owned_by
        delete s.owner_pid
      }
      owned.delete(role)
      s.running = true
      return { ok: true, code: 200 }
    },
  }
}

// A running demo agent answers in uneven bursts (a word, then most of a
// sentence), the way Claude Code delivers text, so streaming can be seen and
// measured without cmdrd.
const DEMO_REPLY =
  "On it. I checked the **stack order** first: #101 sits on #102, so it has to merge first.\n\n" +
  '- Rebased the stack onto `main` and the conflicts were only in the lockfile.\n' +
  '- CI is green on all four PRs.\n' +
  '- One review thread is still open on #103; I answered it and asked for a re-review.\n\n' +
  "Next I'll watch the merge queue and tell you when the last one lands."

// `reply=long`: a reply taller than the view arrives in one burst and the turn
// stays open, so layout checks can measure scrolling at the latest message.
const DEMO_LONG_REPLY = Array.from(
  { length: 24 },
  (_, i) => `${i + 1}. Checked the sample module ${i + 1}: its tests pass and the example output matches the expected file.`,
).join('\n\n')

function streamReply(t: ReplayTransport) {
  const id = `demo-${Date.now()}`
  if (new URLSearchParams(location.search).get('reply') === 'long') {
    t.emit({ kind: 'status', state: 'requesting' })
    setTimeout(() => t.emit({ kind: 'text_delta', message: id, index: 0, text: DEMO_LONG_REPLY }), 300)
    return
  }
  const chunks = DEMO_REPLY.match(/[\s\S]{1,9}|[\s\S]{1,60}/g) ?? []
  const bursts: string[] = []
  for (let i = 0; i < chunks.length; ) {
    const n = 1 + ((i * 7) % 6) // 1-6 chunks per burst, uneven on purpose
    bursts.push(chunks.slice(i, i + n).join(''))
    i += n
  }
  let at = 700
  t.emit({ kind: 'status', state: 'requesting' })
  bursts.forEach((text, i) => {
    at += 90 + ((i * 53) % 260)
    setTimeout(() => t.emit({ kind: 'text_delta', message: id, index: 0, text }), at)
  })
  setTimeout(() => {
    t.emit({ kind: 'text_final', message: id, index: 0, text: DEMO_REPLY })
    t.emit({ kind: 'turn_result', subtype: 'success', isError: false, durationMs: at, costUsd: 3.62 })
  }, at + 300)
}
