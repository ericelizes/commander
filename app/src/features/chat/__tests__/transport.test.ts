import { describe, expect, it, vi } from 'vitest'
import { WsChatTransport } from '../transport'
import type { ChatEvent } from '../events'

class FakeWS {
  static all: FakeWS[] = []
  onopen?: () => void
  onmessage?: (m: { data: string }) => void
  onclose?: () => void
  onerror?: () => void
  closed = false
  constructor(public url: string) {
    FakeWS.all.push(this)
  }
  close() {
    this.closed = true
  }
}

describe('WsChatTransport', () => {
  it('subscribes with since + token, adapts frames, and resumes from the last seq on reconnect', async () => {
    vi.useFakeTimers()
    FakeWS.all = []
    const t = new WsChatTransport({ baseUrl: 'http://127.0.0.1:7777', token: 'tok', WebSocketImpl: FakeWS as never })
    const got: ChatEvent[] = []
    const states: string[] = []
    const unsub = t.subscribe('op:bd-1', 0, (e) => got.push(...e), (s) => states.push(s))
    const ws = FakeWS.all[0]
    expect(ws.url).toBe('ws://127.0.0.1:7777/api/chat/op%3Abd-1/events?since=0&token=tok')
    ws.onopen?.()
    ws.onmessage?.({ data: JSON.stringify({ seq: 4, session: 's', kind: 'status', state: 'requesting' }) })
    ws.onmessage?.({ data: JSON.stringify([{ seq: 5, session: 's', kind: 'text_delta', message: 'm', index: 0, text: 'hi' }]) })
    ws.onmessage?.({ data: 'not json' })
    expect(got).toEqual([]) // history is handed on in one piece, at the marker
    ws.onmessage?.({ data: JSON.stringify({ seq: 5, kind: 'replay_done', source: 'engine' }) })
    expect(got.map((e) => e.seq)).toEqual([4, 5])
    ws.onmessage?.({ data: JSON.stringify({ seq: 6, session: 's', kind: 'text_delta', message: 'm', index: 0, text: '!' }) })
    expect(got.map((e) => e.seq)).toEqual([4, 5, 6]) // live after the marker

    ws.onclose?.()
    vi.advanceTimersByTime(300)
    expect(FakeWS.all[1].url).toContain('since=6')
    expect(states).toEqual(['connecting', 'open', 'closed', 'connecting'])

    unsub()
    expect(FakeWS.all[1].closed).toBe(true)
    vi.useRealTimers()
  })

  it('POSTs commands with a bearer token and surfaces server errors', async () => {
    const calls: { url: string; init: RequestInit }[] = []
    const fetchImpl = (async (url: string, init: RequestInit) => {
      calls.push({ url, init })
      if (url.endsWith('/interrupt')) return new Response(JSON.stringify({ ok: false, error: 'no live process' }), { status: 409 })
      return new Response('{}', { status: 200 })
    }) as typeof fetch
    const t = new WsChatTransport({ baseUrl: 'http://h:1/', token: 'tok', fetchImpl })
    await t.send('commander', { text: 'hi', clientId: 'c1' })
    await t.approve('commander', { id: 'r1', behavior: 'allow', updatedInput: { command: 'ls' } })
    await t.answer('commander', { id: 'q1', answers: { 'Which?': 'A' } })
    expect(calls.map((c) => c.url)).toEqual(['http://h:1/api/chat/commander/send', 'http://h:1/api/chat/commander/approve', 'http://h:1/api/chat/commander/answer'])
    expect((calls[0].init.headers as Record<string, string>).Authorization).toBe('Bearer tok')
    expect(JSON.parse(String(calls[1].init.body))).toEqual({ id: 'r1', decision: 'allow', updated_input: { command: 'ls' } })
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ text: 'hi', client_id: 'c1' })
    await expect(t.interrupt('commander')).rejects.toThrow('no live process')
  })

  it('send reports a mail delivery (a terminal chat), and nothing otherwise', async () => {
    const body = { v: '{"ok":true,"delivered":"mail"}' }
    const fetchImpl = (async () => new Response(body.v, { status: 200 })) as unknown as typeof fetch
    const t = new WsChatTransport({ baseUrl: 'http://h:1', token: 'tok', fetchImpl })
    expect(await t.send('commander', { text: 'hi', clientId: 'c1' })).toEqual({ delivered: 'mail' })
    body.v = '{"ok":true}'
    expect(await t.send('commander', { text: 'hi', clientId: 'c2' })).toEqual({})
  })
})
