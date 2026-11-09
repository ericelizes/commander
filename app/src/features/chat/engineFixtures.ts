// A Manager conversation in cmdrd's wire format (chat.Event frames, as
// GET /api/chat/{role}/events replays them): hydrated transcript history, then
// a live turn. Shapes copied from a real replay; content is made up. Used by
// the engine mapper tests and the app's ?fixture=chat screenshots.

const S = '3d8b665f-0000-4000-8000-000000000001'
let seq = 0
const t0 = Date.parse('2026-10-20T19:44:05Z')

function ev(kind: string, extra: Record<string, unknown>, role: string, source = 'transcript', dt = 0) {
  seq++
  return { seq, role, session_id: S, kind, time: new Date(t0 + seq * 4000 + dt).toISOString(), source, uuid: `u-${seq}`, ...extra }
}

export function managerConversation(role = 'manager-example-app-t03'): Record<string, unknown>[] {
  seq = 0
  const e = (kind: string, extra: Record<string, unknown>, source?: string) => ev(kind, extra, role, source)
  return [
    e('user_message', { text: `You have mail from commander: Rule: only flag the user for real decisions. Run cmdr internal-inbox --agent ${role}`, origin: 'transcript' }),
    e('tool_start', { message_id: 'msg_a', block: 0, tool: { id: 'toolu_a1', name: 'Bash' } }),
    e('tool_input', { message_id: 'msg_a', block: 0, tool: { id: 'toolu_a1', name: 'Bash', input: { command: `cmdr internal-inbox --agent ${role}`, description: 'Read my inbox' } } }),
    e('tool_result', { tool: { id: 'toolu_a1', name: 'Bash', content: '[{"id":"57","from":"commander","subject":"Rule: only flag the user for real decisions"}]' } }),
    e('thinking', { message_id: 'msg_b', block: 0 }),
    e('text', {
      message_id: 'msg_b',
      block: 1,
      text: "Got it. I'll only set a step to **needs-user** when it truly needs you:\n\n- a decision between real options\n- approval for something risky or paid (merge, deploy, prod)\n- access I can't get myself\n\nEverything else I unblock on my own.",
    }),
    e('user_message', { text: 'where are we on the job worker migration?', origin: 'transcript' }),
    e('tool_start', { message_id: 'msg_c', block: 0, tool: { id: 'toolu_c1', name: 'Bash' } }),
    e('tool_input', { message_id: 'msg_c', block: 0, tool: { id: 'toolu_c1', name: 'Bash', input: { command: 'br list --parent example-app-t03 --json | jq -r ".[] | .title + \\" \\" + .status"', description: 'List workstream steps' } } }),
    e('tool_result', { tool: { id: 'toolu_c1', name: 'Bash', content: 'U1 worker skeleton closed\nU2 retry policies in_progress\nU3 per-app pools open' } }),
    e('text', {
      message_id: 'msg_c',
      block: 1,
      text: 'Two of three steps are moving:\n\n| step | state |\n|---|---|\n| U1 worker skeleton | done |\n| U2 retry policies | in progress (Operator on it) |\n| U3 per-app pools | not started |\n\nU2 should land today; nothing needs you yet.',
    }),
    e('session', { session: { model: 'sonnet', cwd: '/tmp/example/code/example-app', cli_version: '2.1.305', resumed: true } }, 'engine'),
    e('session', { session: { model: 'claude-sonnet-5-5', cwd: '/tmp/example/code/example-app', cli_version: '2.1.305', permission_mode: 'bypassPermissions', slash_commands: ['compact', 'context', 'cost'] } }, 'live'),
    e('user_message', { text: 'ok, ping me when U2 merges', origin: 'user' }, 'live'),
    e('status', { status: 'requesting' }, 'live'),
    e('text_delta', { message_id: 'msg_d', block: 0, text: 'Will do. ' }, 'live'),
    e('text', { message_id: 'msg_d', block: 0, text: "Will do. I'll message you the moment the U2 PR merges, with the CI run and the retry numbers." }, 'live'),
    e('rate_limit', { rate_limit: { status: 'allowed', unifiedWindows: { five_hour: { utilization: 0.35, resetsAt: 1792543200 }, seven_day: { utilization: 0.36, resetsAt: 1792598400 } } } }, 'live'),
    e('turn_result', {
      result: {
        subtype: 'success',
        terminal_reason: 'completed',
        num_turns: 1,
        duration_ms: 6334,
        cost_delta_usd: 0.0429,
        total_cost_usd: 3.5982,
        usage: { input_tokens: 4, output_tokens: 145, cache_read_input_tokens: 110643, cache_creation_input_tokens: 2210 },
        model_usage: { 'claude-sonnet-5-5': { contextWindow: 1000000 } },
        permission_denials: [],
      },
    }, 'live'),
    { seq, role, kind: 'replay_done', source: 'engine', time: new Date(t0 + seq * 4000).toISOString() },
  ]
}

// managerConversation plus two mail turns while the user was away (?away=1).
export function managerAwayConversation(role = 'manager-example-app-t03'): Record<string, unknown>[] {
  const base = managerConversation(role)
  const done = base.pop()!
  const e = (kind: string, extra: Record<string, unknown>) => ev(kind, extra, role, 'live')
  return [
    ...base,
    e('user_message', { text: '[mail] operator-u2: U2 PR is up, CI running', origin: 'mail' }),
    e('tool_start', { message_id: 'msg_e', block: 0, tool: { id: 'toolu_e1', name: 'Bash' } }),
    e('tool_result', { tool: { id: 'toolu_e1', name: 'Bash', content: 'CI pending' } }),
    e('text', { message_id: 'msg_e', block: 1, text: 'U2 PR is up; CI is running.' }),
    e('turn_result', { result: { subtype: 'success' } }),
    e('user_message', { text: '[mail] operator-u2: U2 merged', origin: 'mail' }),
    e('tool_start', { message_id: 'msg_f', block: 0, tool: { id: 'toolu_f1', name: 'Bash' } }),
    e('tool_result', { tool: { id: 'toolu_f1', name: 'Bash', content: 'merged' } }),
    e('text', { message_id: 'msg_f', block: 1, text: '**Since you were away**\n\n**U2 retry policies merged.** CI passed on the first run; retries now back off from 1s to 5 min.\n\nNext: U3 per-app pools, Operator starting now.\n\n**Needs you:** [Merge the stack](cmdr:example-app-t03.2), blocked on widget-lib.' }),
    e('turn_result', { result: { subtype: 'success' } }),
    { ...done, seq },
  ]
}

// A turn stuck in Anthropic API retries (shape from commander.jsonl, 2026-10-22):
// notice frames with subtype api_retry and the CLI's object under raw. `now` is
// when the last retry arrived; the first one is `since` ms earlier.
export function apiRetryFrames(role: string, afterSeq: number, now: number, since: number): Record<string, unknown>[] {
  const frame = (n: number, kind: string, extra: Record<string, unknown>, at: number) => ({ seq: afterSeq + n, role, session_id: S, kind, time: new Date(at).toISOString(), source: 'live', uuid: `r-${n}`, ...extra })
  const retry = (n: number, at: number) =>
    frame(n + 2, 'notice', { subtype: 'api_retry', raw: { type: 'system', subtype: 'api_retry', attempt: n, max_retries: 10, retry_delay_ms: 600 * 2 ** (n - 1), error_status: null, error: 'unknown' } }, at)
  return [
    frame(1, 'user_message', { text: 'what is the state of the PR?', origin: 'user' }, now - since),
    frame(2, 'status', { status: 'requesting' }, now - since),
    retry(1, now - since),
    retry(2, now - since / 2),
    retry(3, now),
  ]
}
