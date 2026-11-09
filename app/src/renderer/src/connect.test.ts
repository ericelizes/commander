import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import fixture from '../../../test/fixtures/snapshot.json'
import type { Snapshot } from '../../shared/snapshot'

vi.mock('./host', () => ({
  host: undefined,
  shell: undefined,
  loadConfig: async () => ({ apiUrl: 'http://127.0.0.1:8787', token: 't' }),
}))

const { connect, useStore } = await import('./store')

// FakeSocket stands in for cmdrd's /api/events: the test opens, feeds and
// closes it, like a cmdrd that restarts.
class FakeSocket {
  static all: FakeSocket[] = []
  onopen: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  constructor(public url: URL) {
    FakeSocket.all.push(this)
  }
  open() {
    this.onopen?.()
  }
  snapshot(s: Snapshot) {
    this.onmessage?.({ data: JSON.stringify({ type: 'snapshot', snapshot: s }) })
  }
  drop() {
    this.onclose?.()
  }
  close() {}
}

const snap = fixture as unknown as Snapshot
const last = () => FakeSocket.all[FakeSocket.all.length - 1]
const flush = () => vi.advanceTimersByTimeAsync(0)

describe('connect across a cmdrd restart', () => {
  let stop: () => void
  beforeEach(() => {
    vi.useFakeTimers()
    FakeSocket.all = []
    vi.stubGlobal('WebSocket', FakeSocket)
    useStore.setState({ snapshot: null, conn: 'connecting' })
  })
  afterEach(() => {
    stop?.()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('is live as soon as the new cmdrd accepts, before its cold first snapshot', async () => {
    stop = connect()
    await flush()
    last().open()
    last().snapshot(snap)
    expect(useStore.getState().conn).toBe('live')

    last().drop() // cmdrd restarts
    expect(useStore.getState().conn).toBe('offline')
    await vi.advanceTimersByTimeAsync(500)
    expect(FakeSocket.all).toHaveLength(2)
    last().open() // the new cmdrd answers; its first snapshot is still being built
    expect(useStore.getState().conn).toBe('live')
    expect(useStore.getState().snapshot).not.toBeNull()
  })

  it('first launch: an open socket with no snapshot yet is not live', async () => {
    stop = connect()
    await flush()
    last().open()
    expect(useStore.getState().conn).not.toBe('live')
    last().snapshot(snap)
    expect(useStore.getState().conn).toBe('live')
  })

  it('retries at most 2s apart however long cmdrd is down', async () => {
    stop = connect()
    await flush()
    for (let i = 0; i < 8; i++) {
      last().drop()
      await vi.advanceTimersByTimeAsync(10_000)
    }
    // After 8 refused attempts the wait is still 2s, not the old 10s.
    const n = FakeSocket.all.length
    last().drop()
    await vi.advanceTimersByTimeAsync(2_000)
    expect(FakeSocket.all.length).toBe(n + 1)
  })
})
