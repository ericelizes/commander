import { describe, expect, it } from 'vitest'
import { parseRecording } from '../adapter'
import { deriveStatus, initialState, reduce, reduceAll, type ChatState, type Item, type RequestItem, type SystemItem, type TextItem, type ToolItem, type UserItem } from '../events'
import { FIXTURES, manyEvents } from '../fixtures'
import { toThread, type Activity } from '../thread/toThread'
import { sample } from './samples'

const run = (name: 's1' | 's2' | 's3' | 's4') => reduceAll(initialState(), parseRecording(sample(name)))
const top = (s: ChatState) => s.order.map((id) => s.items[id])
const of = <T extends Item['type']>(s: ChatState, type: T) => top(s).filter((i) => i.type === type) as Extract<Item, { type: T }>[]

// Every content part of every message, flattened.
const unfold = (ps: { type: string; data?: unknown }[]): { type: string; data?: unknown }[] => ps.flatMap((p) => (p.type === 'data-away' ? unfold((p.data as { parts: { type: string; data?: unknown }[] }).parts) : [p]))
const parts = (msgs: ReturnType<typeof toThread>) => unfold(msgs.flatMap((m) => (typeof m.content === 'string' ? [] : (m.content as { type: string; data?: unknown }[]))))

describe('synthetic stream s1 (Bash tool, mail hook, 3 turns)', () => {
  const s = run('s1')

  it('reads model, context window and cumulative cost', () => {
    expect(s.model).toBe('claude-haiku-5-5')
    expect(s.contextWindow).toBe(1_000_000)
    expect(s.costUsd).toBeCloseTo(0.03, 6)
    expect(s.rateLimit?.windows.five_hour.utilization).toBe(0.25)
  })

  it('renders three user bubbles and three turn footers', () => {
    expect(of(s, 'user').map((u) => u.text)).toEqual([
      'List the sample folder with Bash.',
      'Give six lines about a sample library.',
      'Repeat the sample marker.',
    ])
    expect(of(s, 'turn')).toHaveLength(3)
  })

  it('shows hook-injected mail as a system row, not a user bubble', () => {
    const mail = of(s, 'system').filter((x) => x.variant === 'mail')
    expect(mail).toHaveLength(3)
    expect(mail[0].text).toContain('SAMPLE-17')
    expect(of(s, 'user').some((u) => u.text.includes('SAMPLE-17'))).toBe(false)
  })

  it('builds a finished Bash tool card with structured output', () => {
    const [bash] = of(s, 'tool')
    expect(bash.name).toBe('Bash')
    expect(bash.input).toEqual({ command: 'ls', description: 'List files in current directory' })
    expect(bash.status).toBe('done')
    expect(bash.structured).toMatchObject({ stdout: '', interrupted: false })
  })

  it('assembles streamed text blocks and closes them', () => {
    const texts = of(s, 'text')
    expect(texts.map((t) => t.streaming)).toEqual([false, false, false])
    expect(texts[1].text.split('\n')).toHaveLength(6) // the 6-line poem
    expect(texts[2].text).toBe('The sample marker is SAMPLE-17.')
  })

  it('summarizes thinking with duration', () => {
    const th = of(s, 'thinking')
    expect(th.length).toBe(3)
    expect(th[0].done).toBe(true)
    expect(th[0].durationMs).toBe(500)
  })

  it('ends idle and exited', () => {
    expect(s.turnOpen).toBe(false)
    expect(s.alive).toBe(false)
    expect(deriveStatus(s).state).toBe('exited')
  })
})

describe('synthetic stream s2 (permissions, denial, interrupt)', () => {
  const s = run('s2')

  it('accumulates shared-message thinking and tool input before final snapshots', () => {
    let state = initialState()
    const thinking: string[] = []
    const inputs: string[] = []
    for (const e of parseRecording(sample('s2'))) {
      state = reduce(state, e)
      if (e.kind === 'thinking' && e.message === 'message-tool-1') {
        const item = state.items['message-tool-1:0']
        expect(item.type).toBe('thinking')
        if (item.type !== 'thinking') throw new Error('missing thinking block')
        if (e.text && !e.done) {
          thinking.push(item.text)
          expect(item.done).toBe(false)
        }
        if (e.done) {
          expect(thinking).toEqual(['Check the samp', 'Check the sample tool result.'])
          expect(item).toMatchObject({ text: thinking[1], done: true, durationMs: 500 })
        }
      }
      if (e.kind === 'tool_start' && e.id === 'tool-sample-1') {
        expect(e).toMatchObject({ message: 'message-tool-1', index: 1 })
      }
      if (e.kind === 'tool_input_delta' && e.id === 'tool-sample-1') {
        const tool = state.items[e.id] as ToolItem
        inputs.push(tool.inputPartial)
        expect(tool).toMatchObject({ status: 'input', input: {} })
      }
      if (e.kind === 'tool_input' && e.id === 'tool-sample-1') {
        expect(inputs).toEqual(['{"file_path":"/tmp/work/sam', '{"file_path":"/tmp/work/sample.txt","content":"sample"}'])
        expect(state.items[e.id]).toMatchObject({ status: 'running', input: { file_path: '/tmp/work/sample.txt', content: 'sample' } })
      }
    }
    expect(thinking).toHaveLength(2)
    expect(inputs).toHaveLength(2)
    expect(of(state, 'thinking').filter((x) => x.id.startsWith('message-tool-1:'))).toHaveLength(1)
    expect(of(state, 'tool').filter((x) => x.id === 'tool-sample-1')).toHaveLength(1)
    expect(state.items['tool-sample-1']).toMatchObject({ status: 'denied', name: 'Write' })
  })

  it('creates permission cards with suggestions and resolves them from the responses', () => {
    const perms = top(s).filter((i) => i.type === 'permission') as RequestItem[]
    expect(perms.map((p) => p.tool)).toEqual(['Write', 'Bash', 'Bash'])
    expect(perms.map((p) => p.resolved?.behavior)).toEqual(['deny', 'allow', 'allow'])
    expect(perms[1].suggestions[0]).toMatchObject({ type: 'addRules', rules: [{ toolName: 'Bash', ruleContent: 'pwd; ls *' }], destination: 'localSettings' })
    expect(perms[1].blockedPath).toMatch(/made-by-bash\.txt$/)
    expect(s.pendingRequests).toEqual([])
  })

  it('marks the denied Write tool as denied and approved Bash as approved', () => {
    const tools = of(s, 'tool')
    expect(tools[0]).toMatchObject({ name: 'Write', status: 'denied', approval: 'deny' })
    expect(tools[1]).toMatchObject({ name: 'Bash', status: 'done', approval: 'allow' })
  })

  it('hides resolved permission rows that the tool step already shows', () => {
    const items = parts(toThread(s, false)).filter((p) => p.type === 'data-item').map((p) => (p.data as { type: string }).type)
    expect(items).not.toContain('permission')
  })

  it('shows the interrupt as a divider and a stopped turn', () => {
    expect(of(s, 'system').filter((x) => x.variant === 'interrupted')).toHaveLength(1)
    const stopped = of(s, 'turn').find((t) => t.terminalReason === 'aborted_streaming')
    expect(stopped).toMatchObject({ isError: true, subtype: 'error_during_execution' })
    expect(of(s, 'text').every((t) => !t.streaming)).toBe(true)
  })

  it('takes per-turn cost deltas from the cumulative total', () => {
    const deltas = of(s, 'turn').map((t) => t.costDelta ?? 0)
    expect(deltas.reduce((a, b) => a + b, 0)).toBeCloseTo(s.costUsd, 8)
    expect(s.costUsd).toBeCloseTo(0.03, 8)
  })
})

describe('synthetic stream s3 (stop-hook mail, mid-turn steer, SIGINT)', () => {
  const s = run('s3')

  it('hides Stop hook feedback, which is written for the agent', () => {
    expect(of(s, 'system').some((x) => x.variant === 'hook')).toBe(false)
    expect(of(s, 'system').some((x) => x.variant === 'warn' && x.text === 'Stop hook error occurred')).toBe(true)
  })

  it('queues the mid-turn STEER message and runs it as the next turn', () => {
    const states: boolean[] = []
    let st = initialState()
    for (const e of parseRecording(sample('s3'))) {
      st = reduce(st, e)
      const steer = Object.values(st.items).find((i): i is UserItem => i.type === 'user' && i.text.startsWith('STEER'))
      if (steer) states.push(!!steer.queued)
    }
    expect(states[0]).toBe(true) // queued when it arrived
    expect(states[states.length - 1]).toBe(false) // unqueued once its turn began
  })
})

describe('synthetic stream s4 (/context, /compact, /cost)', () => {
  const s = run('s4')

  it('adds a compaction marker with token counts', () => {
    const c = of(s, 'system').find((x) => x.variant === 'compact') as SystemItem
    expect(c.detail).toBe('40k → 14k tokens (manual)')
    expect(s.compactions).toBe(1)
    expect(s.contextTokens).toBeGreaterThan(0)
  })

  it('shows the compaction summary collapsed, not as a user bubble', () => {
    expect(of(s, 'system').some((x) => x.variant === 'summary' && x.detail?.includes('continued from a previous conversation'))).toBe(true)
    expect(of(s, 'user').map((u) => u.text)).toEqual(['/context', '/compact', '/cost', 'What happened to sample.txt?'])
  })

  it('does not add turn footers for local slash commands', () => {
    expect(of(s, 'turn')).toHaveLength(1)
  })
})

describe('fixtures', () => {
  it.each(Object.keys(FIXTURES))('%s reduces to a non-empty timeline', (name) => {
    const s = reduceAll(initialState(), FIXTURES[name as keyof typeof FIXTURES]())
    expect(s.order.length).toBeGreaterThan(2)
    expect(toThread(s, false).length).toBeGreaterThan(1)
  })

  it('nests sub-agent events under the Task card', () => {
    const s = reduceAll(initialState(), FIXTURES.subAgent())
    const task = s.items.task1 as ToolItem
    expect(task.children).toHaveLength(5)
    expect(s.order).not.toContain('sg1')
    expect(task.task?.status).toBe('running')
    expect(deriveStatus(s).subAgents).toBe(1)
  })

  it('folds a run of consecutive tool calls into one live activity row', () => {
    const s = reduceAll(initialState(), FIXTURES.toolHeavy())
    const rows = parts(toThread(s, true)).filter((p) => p.type === 'data-activity').map((p) => p.data as Activity)
    expect(rows.length).toBeGreaterThan(0)
    expect(rows[0].steps.filter((st) => st.type === 'tool').map((st) => st.id)).toEqual(['g1', 'r1', 'r2', 'b1'])
    expect(rows[rows.length - 1].active).toBe(true)
  })

  it('reports the running tool in the status strip', () => {
    const s = reduceAll(initialState(), FIXTURES.toolHeavy())
    expect(deriveStatus(s)).toMatchObject({ state: 'working', detail: 'Run the full suite' })
  })

  it('reports a pending approval as "needs you"', () => {
    const s = reduceAll(initialState(), FIXTURES.permissionPrompt())
    expect(deriveStatus(s)).toMatchObject({ state: 'waiting', detail: 'approval: Bash' })
    expect((s.items['req-p2'] as RequestItem).suggestions).toHaveLength(2)
  })
})

describe('reducer mechanics', () => {
  it('replaces an optimistic bubble with the engine echo', () => {
    let s = reduce(initialState(), { kind: 'local_send', clientId: 'c1', text: 'hello' })
    expect((s.items['local-c1'] as UserItem).pending).toBe(true)
    s = reduce(s, { kind: 'user_message', seq: 1, session: 'x', id: 'u1', text: 'hello', clientId: 'c1' })
    expect(s.order).toEqual(['u1'])
    expect((s.items.u1 as UserItem).pending).toBeUndefined()
  })

  it('ignores duplicate seqs after a reconnect', () => {
    const e = { kind: 'text_delta' as const, seq: 5, session: 'x', message: 'm', index: 0, text: 'ab' }
    const s = reduceAll(initialState(), [e, e])
    expect((s.items['m:0'] as TextItem).text).toBe('ab')
  })

  it('treats [mail] user messages as system rows', () => {
    const s = reduce(initialState(), { kind: 'user_message', seq: 1, session: 'x', id: 'u', text: '[mail] from lead: ship it' })
    expect(top(s)[0]).toMatchObject({ type: 'system', variant: 'mail', text: '[mail] from lead: ship it' })
  })

  it('keeps unchanged items referentially stable across a batch', () => {
    const s1 = reduceAll(initialState(), FIXTURES.longAnswer())
    const s2 = reduce(s1, { kind: 'text_delta', seq: 10_000, session: 'x', message: 'm2', index: 0, text: ' more' })
    expect(s2.items.u1).toBe(s1.items.u1)
    expect(s2.items['m2:0']).not.toBe(s1.items['m2:0'])
  })

  // Best of three: a load spike hits one run, a real slowdown hits all of them.
  const bestOf = (runs: number, fn: () => void) => {
    let best = Infinity
    for (let i = 0; i < runs; i++) {
      const t0 = performance.now()
      fn()
      best = Math.min(best, performance.now() - t0)
    }
    return best
  }

  it('folds 10k events quickly', () => {
    const evs = manyEvents(10_000)
    let s = initialState()
    const ms = bestOf(3, () => {
      s = reduceAll(initialState(), evs)
    })
    expect(s.order.length).toBeGreaterThan(3000)
    expect(ms).toBeLessThan(4000)
  })

  it('folds 10k events one by one (live stream) in reasonable time', () => {
    const evs = manyEvents(10_000)
    let s = initialState()
    const ms = bestOf(3, () => {
      s = initialState()
      for (let i = 0; i < evs.length; i += 20) s = reduceAll(s, evs.slice(i, i + 20)) // ~one rAF batch each
    })
    expect(ms).toBeLessThan(20_000)
    expect(s.lastSeq).toBe(10_000)
  })
})
