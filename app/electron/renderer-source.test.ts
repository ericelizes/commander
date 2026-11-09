import { describe, expect, it, vi } from 'vitest'
import { devRendererURL, devServerUp, sourceLabel, watchRendererSource, type SourceWindow } from './renderer-source'

function fakeWin() {
  const loads: string[] = []
  let failLoad: ((e: unknown, code: number, desc: string, url: string, main: boolean) => void) | undefined
  const win: SourceWindow & { failURL?: string } = {
    isDestroyed: () => false,
    loadURL: vi.fn(async (u: string) => {
      loads.push(u)
      if (win.failURL === u) throw new Error('ERR_CONNECTION_REFUSED')
    }),
    loadFile: vi.fn(async (p: string) => {
      loads.push(`file:${p}`)
    }),
    webContents: { on: (_ev, l) => (failLoad = l) },
  }
  return { win, loads, fail: (code: number, url: string) => failLoad?.({}, code, '', url, true) }
}

const DEV = 'http://127.0.0.1:5173/app/'
const flush = () => new Promise((r) => setTimeout(r, 0))

describe('watchRendererSource', () => {
  it('loads the dev server when it answers at startup', async () => {
    const { win, loads } = fakeWin()
    const changes: string[] = []
    const w = watchRendererSource(win, { devUrl: DEV, bundle: '/dist/index.html', probe: async () => true, onChange: (s) => changes.push(s), setInterval: () => 0, clearInterval: () => {} })
    await flush()
    expect(loads).toEqual([DEV])
    expect(w.source()).toBe('live')
    expect(changes).toEqual(['live'])
  })

  it('shows the bundle, then switches to live when the dev server comes up, and back when it dies', async () => {
    const { win, loads } = fakeWin()
    let up = false
    const w = watchRendererSource(win, { devUrl: DEV, bundle: '/dist/index.html', probe: async () => up, setInterval: () => 0, clearInterval: () => {} })
    await flush()
    expect(loads).toEqual(['file:/dist/index.html'])
    await w.tick()
    expect(loads).toHaveLength(1) // still down: no reload of the bundle
    up = true
    await w.tick()
    expect(loads.at(-1)).toBe(DEV)
    await w.tick()
    expect(loads).toHaveLength(2) // still live: no reload
    up = false
    for (let i = 0; i < 3; i++) await w.tick()
    expect(w.source()).toBe('live') // a few missed probes (a restart) keep the live page
    await w.tick()
    expect(loads.at(-1)).toBe('file:/dist/index.html') // the fourth in a row: it is gone
    expect(w.source()).toBe('bundled')
  })

  it('falls back to the bundle when the live page fails to load', async () => {
    const { win, loads, fail } = fakeWin()
    const w = watchRendererSource(win, { devUrl: DEV, bundle: '/b', probe: async () => true, setInterval: () => 0, clearInterval: () => {} })
    await flush()
    fail(-3, DEV) // aborted by a newer load: ignored
    expect(w.source()).toBe('live')
    fail(-102, DEV)
    await flush()
    expect(loads.at(-1)).toBe('file:/b')
    expect(w.source()).toBe('bundled')
  })

  it('a refused loadURL lands on the bundle', async () => {
    const { win, loads } = fakeWin()
    win.failURL = DEV
    const w = watchRendererSource(win, { devUrl: DEV, bundle: '/b', probe: async () => true, setInterval: () => 0, clearInterval: () => {} })
    await flush()
    await flush()
    expect(loads).toEqual([DEV, 'file:/b'])
    expect(w.source()).toBe('bundled')
  })

  it('polls every 3s and stops', async () => {
    const { win } = fakeWin()
    const every = vi.fn(() => 7)
    const cancel = vi.fn()
    const w = watchRendererSource(win, { devUrl: DEV, bundle: '/b', probe: async () => false, setInterval: every, clearInterval: cancel })
    expect(every).toHaveBeenCalledWith(expect.any(Function), 3000)
    w.stop()
    expect(cancel).toHaveBeenCalledWith(7)
  })
})

describe('devServerUp', () => {
  const res = (status: number, body: string) => (async () => new Response(body, { status })) as unknown as typeof fetch
  it('wants a Vite page', async () => {
    expect(await devServerUp(DEV, res(200, '<script type="module" src="/app/@vite/client"></script>'))).toBe(true)
    expect(await devServerUp(DEV, res(200, 'some other server'))).toBe(false)
    expect(await devServerUp(DEV, res(404, '/@vite/client'))).toBe(false)
    expect(await devServerUp(DEV, (async () => Promise.reject(new Error('refused'))) as unknown as typeof fetch)).toBe(false)
  })
})

describe('devRendererURL', () => {
  it('defaults to 127.0.0.1:5173/app/, takes an override, and can be turned off', () => {
    expect(devRendererURL({})).toBe(DEV)
    expect(devRendererURL({ COMMANDER_APP_DEV_URL: 'http://127.0.0.1:6000/' })).toBe('http://127.0.0.1:6000/app/')
    expect(devRendererURL({ COMMANDER_APP_DEV_URL: 'off' })).toBeUndefined()
    expect(sourceLabel('live')).toBe('Live (dev server)')
    expect(sourceLabel('bundled')).toBe('Bundled')
  })
})
