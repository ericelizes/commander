import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { ChatEvent } from '../events'
import type { ChatTransport } from '../transport'
import { useChat } from '../useChat'

// 3000 messages, one user line each: seq n is "message n".
const all: ChatEvent[] = Array.from({ length: 3000 }, (_, i) => ({ kind: 'user_message', seq: i + 1, session: 's', id: `u${i + 1}`, text: `message ${i + 1}`, history: true }) as ChatEvent)

function transport(): ChatTransport & { sinces: number[] } {
  const sinces: number[] = []
  return {
    sinces,
    subscribe(_role, since, onEvent) {
      sinces.push(since)
      setTimeout(() => onEvent(all.filter((e) => e.seq > since)), 0)
      return () => {}
    },
    history: async (_role, since, until) => all.filter((e) => e.seq > since && e.seq <= until),
    send: async () => {},
    interrupt: async () => {},
    approve: async () => {},
    answer: async () => {},
  }
}

const texts = (s: ReturnType<typeof useChat>['state']) => s.order.map((id) => (s.items[id] as { text?: string }).text)

describe('a chat opens with its recent history', () => {
  it('loads only the last part, then earlier pages on request, in order', async () => {
    const t = transport()
    const { result } = renderHook(() => useChat('commander', t, { lastSeq: 3000 }))
    await waitFor(() => expect(result.current.state.order.length).toBe(2000))
    expect(t.sinces).toEqual([1000])
    expect(texts(result.current.state)[0]).toBe('message 1001')
    expect(result.current.earlier.has).toBe(true)

    act(() => result.current.earlier.load())
    await waitFor(() => expect(result.current.state.order.length).toBe(3000))
    expect(texts(result.current.state).slice(0, 2)).toEqual(['message 1', 'message 2'])
    expect(texts(result.current.state).at(-1)).toBe('message 3000')
    expect(result.current.earlier.has).toBe(false)
  })

  it('a sent message survives loading earlier history', async () => {
    const t = transport()
    const { result } = renderHook(() => useChat('commander', t, { lastSeq: 3000 }))
    await waitFor(() => expect(result.current.state.order.length).toBe(2000))
    await act(() => result.current.send('hello'))
    act(() => result.current.earlier.load())
    await waitFor(() => expect(result.current.state.order.length).toBe(3001))
    expect(texts(result.current.state).at(-1)).toBe('hello')
  })
})
