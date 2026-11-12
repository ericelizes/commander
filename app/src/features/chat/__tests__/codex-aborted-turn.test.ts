import { describe, expect, it } from 'vitest'
import { EngineMapper } from '../engine'
import { initialState, reduceAll } from '../events'
import { toThread } from '../thread/toThread'

function replay(queued: boolean, terminalReason = 'aborted_streaming') {
  const base = { role: 'commander', session_id: 's', source: 'live' }
  const frames = [
    { ...base, seq: 1, kind: 'session', session: { model: 'codex:gpt-6.1-sol' } },
    { ...base, seq: 2, kind: 'user_message', uuid: 'u1', origin: 'user', text: 'Can we roll forward?', time: '2026-11-03T16:54:43Z' },
    ...(queued ? [{ ...base, seq: 3, kind: 'user_message', uuid: 'u2', origin: 'user', text: 'hello', time: '2026-11-03T16:54:44Z' }] : []),
    { ...base, seq: 4, kind: 'turn_result', result: { subtype: 'error_during_execution', terminal_reason: terminalReason, duration_ms: 3892 }, time: '2026-11-03T16:54:47Z' },
    { ...base, seq: 5, kind: 'agent_state', state: { state: 'idle' } },
  ]
  return reduceAll(initialState(), new EngineMapper().map(frames, 'commander'))
}

describe('one marker for a stopped turn', () => {
  const base = { role: 'commander', session_id: 's', source: 'live' }
  const run = (withDivider: boolean) => reduceAll(initialState(), new EngineMapper().map([
    { ...base, seq: 1, kind: 'user_message', uuid: 'u1', origin: 'user', text: 'recap please' },
    { ...base, seq: 2, kind: 'text_delta', message_id: 'm1', text: 'Working' },
    ...(withDivider ? [{ ...base, seq: 3, kind: 'notice', subtype: 'interrupted' }] : []),
    { ...base, seq: 4, kind: 'turn_result', result: { subtype: 'error_during_execution', terminal_reason: 'aborted_streaming', duration_ms: 12000 } },
  ], 'commander'))

  it('marks the stopped turn as already shown when the Interrupted divider is there', () => {
    const state = run(true)
    const turn = Object.values(state.items).find(it => it.type === 'turn')
    expect(turn && turn.type === 'turn' && turn.shownAsInterrupted).toBe(true)
  })

  it('leaves the stopped turn to draw the divider itself when none came', () => {
    const state = run(false)
    const turn = Object.values(state.items).find(it => it.type === 'turn')
    expect(turn && turn.type === 'turn' && !turn.shownAsInterrupted).toBe(true)
  })
})

describe('Codex aborted turns', () => {
  it('shows a stopped reply even when no assistant text or tools arrived', () => {
    const state = replay(false)
    const thread = toThread(state, state.turnOpen)
    expect(thread.map(m => m.role)).toEqual(['user', 'assistant'])
    expect(thread.at(-1)?.status).toEqual({ type: 'complete', reason: 'stop' })
    expect(JSON.stringify(thread.at(-1)?.content)).toContain('aborted_streaming')
    expect(state.turnOpen).toBe(false)
  })

  it('does not invent another thinking turn for a message sent before the abort', () => {
    const state = replay(true)
    expect(state.turnOpen).toBe(false)
    expect(state.turnStartTs).toBeUndefined()
    expect(state.queue).toEqual([])
    expect(state.order.map(id => state.items[id].type)).toEqual(['user', 'user', 'turn'])
    const thread = toThread(state, state.turnOpen)
    expect(thread.map(m => m.role)).toEqual(['user', 'user', 'assistant'])
    expect(thread.some(m => m.status?.type === 'running')).toBe(false)
    expect(JSON.stringify(thread.at(-1)?.content)).toContain('aborted_streaming')
  })
})

it('honors backend idle after a steered message without moving it below the answer', () => {
  const base = { role: 'commander', session_id: 's', source: 'live' }
  const state = reduceAll(initialState(), new EngineMapper().map([
    { ...base, seq: 1, kind: 'user_message', uuid: 'u1', origin: 'user', text: 'hello' },
    { ...base, seq: 2, kind: 'text_delta', message_id: 'm1', text: 'Answering' },
    { ...base, seq: 3, kind: 'user_message', uuid: 'u2', origin: 'user', text: 'Do you see me?' },
    { ...base, seq: 4, kind: 'text', message_id: 'm1', text: 'Yes, I see you.' },
    { ...base, seq: 5, kind: 'turn_result', result: { subtype: 'success' } },
    { ...base, seq: 6, kind: 'agent_state', state: { state: 'idle' } },
  ]))
  expect(state.turnOpen).toBe(false)
  expect(state.turnStartTs).toBeUndefined()
  expect(state.queue).toEqual([])
  expect(state.order.map(id => state.items[id].type)).toEqual(['user', 'text', 'user', 'turn'])
  expect(toThread(state, state.turnOpen).some(m => m.status?.type === 'running')).toBe(false)
})
