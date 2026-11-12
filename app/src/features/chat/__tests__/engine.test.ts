import { describe, expect, it } from 'vitest'
import { EngineMapper } from '../engine'
import { managerConversation } from '../engineFixtures'
import { initialState, reduceAll, type SystemItem, type TextItem, type ToolItem, type UserItem } from '../events'
import { parseMail } from '../mail'
import { toThread, type Activity } from '../thread/toThread'

describe('EngineMapper (cmdrd chat.Event frames)', () => {
  it('renders a hydrated transcript replay as the conversation history', () => {
    const evs = new EngineMapper().map(managerConversation('manager-x'), 'manager-x')
    const s = reduceAll(initialState(), evs)
    const items = s.order.map((id) => s.items[id])
    const types = items.map((i) => (i.type === 'system' ? `system:${i.variant}` : i.type))
    expect(types).toEqual(['system:mail', 'tool', 'thinking', 'text', 'user', 'tool', 'text', 'user', 'text', 'turn'])
    const bash = items[1] as ToolItem
    expect(bash).toMatchObject({ name: 'Bash', status: 'done', input: { description: 'Read my inbox' } })
    expect((items[3] as TextItem).text).toContain('needs-user')
    expect((items[4] as UserItem).text).toBe('where are we on the job worker migration?')
    expect((items[8] as TextItem).text).toMatch(/^Will do\. I'll message you/) // final text replaces the delta
    expect(s.model).toBe('claude-sonnet-5-5')
    expect(s.permissionMode).toBe('bypassPermissions')
    expect(s.costUsd).toBeCloseTo(3.5982)
    expect(s.contextWindow).toBe(1000000)
    expect(s.rateLimit?.windows.five_hour.utilization).toBe(0.35)
    expect(s.turnOpen).toBe(false)
    expect(s.lastSeq).toBe(19)
  })

  it('keys tool_input_delta frames (no tool id) by message + block', () => {
    const m = new EngineMapper()
    const base = { role: 'r', session_id: 's', source: 'live', message_id: 'msg', block: 2 }
    m.map({ ...base, seq: 1, kind: 'tool_start', tool: { id: 'toolu_1', name: 'Bash' } })
    const [d] = m.map({ ...base, seq: 2, kind: 'tool_input_delta', tool: { id: '', partial_json: '{"command":' } })
    expect(d).toMatchObject({ kind: 'tool_input_delta', id: 'toolu_1', partial: '{"command":' })
  })

  it('maps requests and their resolution', () => {
    const m = new EngineMapper()
    const b = { role: 'r', session_id: 's', source: 'live' }
    const evs = m.map([
      { ...b, seq: 1, kind: 'permission_request', request: { id: 'q1', tool_name: 'Bash', tool_use_id: 't1', input: { command: 'rm -rf x' }, suggestions: [] } },
      { ...b, seq: 2, kind: 'request_resolved', request: { id: 'q1', decision: 'allow_session' } },
      { ...b, seq: 3, kind: 'question', request: { id: 'q2', input: { questions: [{ question: 'Which?', options: [{ label: 'A' }] }] } } },
      { ...b, seq: 4, kind: 'plan', request: { id: 'q3', input: { plan: '1. do it' } } },
    ])
    expect(evs).toMatchObject([
      { kind: 'permission_request', id: 'q1', tool: 'Bash', toolUseId: 't1' },
      { kind: 'request_resolved', id: 'q1', behavior: 'allow', always: true },
      { kind: 'question', id: 'q2', questions: [{ question: 'Which?' }] },
      { kind: 'plan', id: 'q3', plan: '1. do it' },
    ])
  })

  it('drops engine-only kinds and passes normalized frames through', () => {
    const m = new EngineMapper()
    expect(m.map({ seq: 9, role: 'r', kind: 'replay_done', source: 'engine' })).toEqual([])
    expect(m.map({ seq: 3, role: 'r', session_id: 's', source: 'engine', kind: 'agent_state', state: { state: 'idle' } })).toMatchObject([{ kind: 'agent_idle' }])
    expect(m.map({ seq: 4, session: 's', kind: 'status', state: 'requesting' })).toMatchObject([{ kind: 'status', state: 'requesting' }])
  })

  it('shows no row for a tool permission hook (the failed tool step says it)', () => {
    const m = new EngineMapper()
    const output = JSON.stringify({ hookSpecificOutput: { hookEventName: 'PreToolUse', permissionDecision: 'deny', permissionDecisionReason: 'Managers do not do the work.' } })
    expect(m.map({ seq: 5, role: 'r', kind: 'hook', hook: { name: 'PreToolUse', output } })).toEqual([])
  })

  it('shows a Stop hook mail cut off at 4000 bytes as mail, not as its JSON', () => {
    const reason = 'Commander mail system: new mail for you (delivered automatically; the user did not type this).\n\n[mail] from claude-code-user: You own the Graph: gate -> work -> gate\n' + 'x'.repeat(5000)
    const output = JSON.stringify({ decision: 'block', reason }).slice(0, 4000)
    const evs = new EngineMapper().map({ seq: 8, role: 'commander', kind: 'hook', hook: { name: 'Stop', output } })
    expect(evs).toMatchObject([{ kind: 'user_message', origin: 'mail' }])
    const text = (evs[0] as { text: string }).text
    expect(parseMail(text).mails).toMatchObject([{ from: 'claude-code-user', subject: 'You own the Graph: gate -> work -> gate' }])
    expect(text).not.toContain('"decision"')
  })

  // Recorded 2026-10-21: the user's "great" at 10:46, idle for two hours, then
  // mail woke Commander and the mail Stop hook delivered more mail.
  it('shows mail delivered by the Stop hook as mail, and times the work from the mail', () => {
    const b = { role: 'commander', session_id: 's', source: 'live' }
    const at = (hms: string) => `2026-10-21T${hms}-04:00`
    const reason = 'Commander mail system: new mail for you (delivered automatically; the user did not type this).\n\n[mail] from manager-x: Cannot spawn Operators'
    const evs = new EngineMapper().map([
      { ...b, seq: 1, kind: 'user_message', time: at('10:46:27'), text: 'great', origin: 'user' },
      { ...b, seq: 2, kind: 'text', time: at('10:46:28'), message_id: 'm0', block: 0, text: 'On it.' },
      { ...b, seq: 3, kind: 'turn_result', time: at('10:46:28'), result: { subtype: 'success' } },
      { ...b, seq: 4, kind: 'user_message', time: at('12:36:01'), text: '[mail] manager-x: spawn failed', origin: 'mail' },
      { ...b, seq: 5, kind: 'tool_start', time: at('12:36:03'), message_id: 'm1', block: 0, tool: { id: 't1', name: 'Bash' } },
      { ...b, seq: 6, kind: 'tool_result', time: at('12:36:07'), tool: { id: 't1', output: 'ok' } },
      { ...b, seq: 7, kind: 'text', time: at('12:36:24'), message_id: 'm2', block: 0, text: 'Handled.' },
      { ...b, seq: 8, kind: 'hook', time: at('12:36:25'), hook: { name: 'Stop', output: JSON.stringify({ decision: 'block', reason }) } },
      { ...b, seq: 9, kind: 'user_message', time: at('12:36:25'), text: 'Stop hook feedback:\n' + reason },
      { ...b, seq: 10, kind: 'notice', subtype: 'notification', time: at('12:36:25'), text: 'Stop hook error occurred · ctrl+o to see' },
      { ...b, seq: 11, kind: 'text', time: at('12:36:28'), message_id: 'm3', block: 0, text: 'Already handled.' },
    ])
    const s = reduceAll(initialState(), evs)
    const systems = s.order.map((id) => s.items[id]).filter((i): i is SystemItem => i.type === 'system')
    expect(systems.map((i) => [i.variant, parseMail(i.text).mails])).toEqual([
      ['mail', [{ from: 'manager-x', subject: 'spawn failed' }]],
      ['mail', [{ from: 'manager-x', subject: 'Cannot spawn Operators' }]],
    ])
    const all = toThread(s, false).flatMap((m) => (typeof m.content === 'string' ? [] : (m.content as { type: string; data?: unknown }[])))
    const activity = all
      .flatMap((p) => (p.type === 'data-away' ? (p.data as { parts: { type: string; data?: unknown }[] }).parts : [p]))
      .filter((p) => p.type === 'data-activity')
      .map((p) => p.data as Activity)
    expect(activity).toHaveLength(1)
    expect(activity[0].endedAt! - activity[0].startedAt!).toBeLessThan(60_000)
  })

  // Recorded 2026-11-12 (commander.jsonl seq 1508910-1509017): a question sent
  // while the Commander finished a wrap-up; the turn ended without reading it
  // and the next turn answered it.
  it('puts a message the turn never read after that turn, before the reply to it', () => {
    const b = { role: 'commander', session_id: 's', source: 'live' }
    const at = (hms: string) => `2026-11-12T${hms}Z`
    const m = new EngineMapper()
    const evs = m.map([
      { ...b, seq: 1, kind: 'user_message', source: 'engine', time: at('22:40:00'), text: '[mail] supervision: three alerts', origin: 'mail' },
      { ...b, seq: 1.5, kind: 'hook', time: at('22:40:00'), hook: { name: 'UserPromptSubmit', event: 'UserPromptSubmit' } },
      { ...b, seq: 2, kind: 'tool_start', time: at('22:40:20'), message_id: 'm1', block: 0, tool: { id: 't1', name: 'Bash' } },
      { ...b, seq: 3, kind: 'tool_result', time: at('22:40:50'), tool: { id: 't1', output: 'ok' } },
      { ...b, seq: 4, kind: 'user_message', source: 'engine', time: at('22:40:57'), text: 'what is the accepted data latency', origin: 'user' },
      { ...b, seq: 5, kind: 'text', time: at('22:40:57'), message_id: 'm2', block: 0, text: 'I handled all three supervision alerts.' },
      { ...b, seq: 6, kind: 'turn_result', time: at('22:40:57'), result: { subtype: 'success' } },
      { ...b, seq: 7, kind: 'text', time: at('22:41:15'), message_id: 'm3', block: 0, text: 'Yes. A chart can change in CareHub.' },
    ])
    const s = reduceAll(initialState(), evs)
    const order = s.order.map((id) => s.items[id]).map((i) => (i.type === 'user' ? i.text : i.type === 'text' ? i.text : i.type === 'system' ? i.variant : i.type))
    const q = order.indexOf('what is the accepted data latency')
    expect(q).toBeGreaterThan(order.indexOf('I handled all three supervision alerts.'))
    expect(q).toBeLessThan(order.indexOf('Yes. A chart can change in CareHub.'))
    expect(s.queue).toEqual([])
  })

  // Recorded 2026-10-21 (commander.jsonl seq 3242-3751): two messages sent
  // while a mail-woken turn ran a tool; the CLI read both at the next tool
  // boundary (one UserPromptSubmit hook each) and answered them in that turn.
  it('puts messages sent mid-turn where the agent read them, before its reply', () => {
    const b = { role: 'commander', session_id: 's', source: 'live' }
    const at = (hms: string) => `2026-10-21T${hms}-04:00`
    const read = (seq: number, hms: string) => ({ ...b, seq, kind: 'hook', time: at(hms), hook: { name: 'UserPromptSubmit', event: 'UserPromptSubmit' } })
    const m = new EngineMapper()
    const evs = m.map([
      { ...b, seq: 1, kind: 'user_message', source: 'engine', time: at('12:44:26'), text: '[mail] manager-x: restarts stopped', origin: 'mail' },
      read(2, '12:44:26'),
      { ...b, seq: 3, kind: 'tool_start', time: at('12:44:35'), message_id: 'm1', block: 0, tool: { id: 't1', name: 'Bash' } },
      { ...b, seq: 4, kind: 'user_message', source: 'engine', time: at('12:44:50'), text: 'yeah do it', origin: 'user' },
      { ...b, seq: 5, kind: 'user_message', source: 'engine', time: at('12:45:16'), text: 'also why queued', origin: 'user' },
      { ...b, seq: 6, kind: 'tool_result', time: at('12:45:30'), tool: { id: 't1', output: 'ok' } },
      read(7, '12:45:30'),
      read(8, '12:45:30'),
      { ...b, seq: 9, kind: 'text', time: at('12:46:24'), message_id: 'm2', block: 0, text: 'The streaming fix: done.' },
      { ...b, seq: 10, kind: 'turn_result', time: at('12:46:24'), result: { subtype: 'success' } },
    ])
    const mid = reduceAll(initialState(), evs.filter((e) => e.seq <= 5))
    expect(mid.queue).toHaveLength(2) // sent, not read yet
    const s = reduceAll(initialState(), evs)
    const order = s.order.map((id) => s.items[id]).map((i) => (i.type === 'user' ? i.text : i.type === 'text' ? i.text : i.type === 'system' ? i.variant : i.type))
    expect(order).toEqual(['mail', 'tool', 'yeah do it', 'also why queued', 'The streaming fix: done.', 'turn'])
    expect(s.queue).toEqual([])
    expect(s.order.map((id) => s.items[id]).some((i) => i.type === 'user' && i.queued)).toBe(false)
    expect(s.turnOpen).toBe(false)
  })

  // Recorded 2026-10-22 (commander.jsonl seq 33395-33397): a turn stuck in API retries.
  it('shows API retries until real output resumes', () => {
    const b = { role: 'commander', session_id: 's', source: 'live' }
    const at = (sec: number) => `2026-10-22T09:49:${String(sec).padStart(2, '0')}-04:00`
    const retry = (seq: number, attempt: number, sec: number, ms: number) => ({ ...b, seq, kind: 'notice', time: at(sec), subtype: 'api_retry', raw: { type: 'system', subtype: 'api_retry', attempt, max_retries: 10, retry_delay_ms: ms, error_status: null, error: 'unknown' } })
    const m = new EngineMapper()
    const evs = m.map([
      { ...b, seq: 1, kind: 'user_message', source: 'engine', time: at(0), text: 'hi', origin: 'user' },
      retry(2, 1, 0, 603),
      retry(3, 2, 1, 1116),
    ])
    expect(evs.map((e) => e.kind)).toEqual(['user_message', 'api_retry', 'api_retry'])
    const s = reduceAll(initialState(), evs)
    expect(s.apiRetry).toMatchObject({ attempt: 2, maxRetries: 10, delayMs: 1116, firstTs: Date.parse(at(0)), ts: Date.parse(at(1)) })
    expect(s.order.map((id) => s.items[id].type)).toEqual(['user']) // no system rows
    const back = reduceAll(s, m.map({ ...b, seq: 4, kind: 'text', time: at(5), message_id: 'm1', block: 0, text: 'hello' }))
    expect(back.apiRetry).toBeUndefined()
    const ended = reduceAll(s, m.map({ ...b, seq: 4, kind: 'turn_result', time: at(5), result: { subtype: 'success' } }))
    expect(ended.apiRetry).toBeUndefined()
  })
})
