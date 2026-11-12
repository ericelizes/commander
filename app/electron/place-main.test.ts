import { describe, expect, it, vi } from 'vitest'
import { installPlaceQuit, terminatePlaceGroup } from './place-main'

describe('place quit cleanup', () => {
  it('force kills a TERM-ignoring group before quitting, even after the leader exits', async () => {
    vi.useFakeTimers()
    try {
      let beforeQuit!: (ev: { preventDefault(): void }) => void
      const order: string[] = []
      let alive = true
      const kill = vi.fn((_pid: number, sig?: NodeJS.Signals | number) => {
        if (!alive) throw Object.assign(new Error('gone'), { code: 'ESRCH' })
        if (sig === 'SIGKILL') { order.push('kill'); alive = false }
        return true
      })
      const app = { on: vi.fn((_ev, cb) => { beforeQuit = cb }), quit: () => { order.push('quit') }, getPath: () => '', getVersion: () => '' }
      const runner = { active: () => [{ runId: 7, step: 'Example step' }], stopAll: () => terminatePlaceGroup(42, kill as typeof process.kill) }
      installPlaceQuit(app as any, { showMessageBox: vi.fn(async () => ({ response: 1 })) } as any, runner, () => order.push('close'), vi.fn())
      const preventDefault = vi.fn()
      beforeQuit({ preventDefault })
      await vi.advanceTimersByTimeAsync(1000)
      expect(order).toEqual([])
      beforeQuit({ preventDefault })
      await vi.advanceTimersByTimeAsync(4000)
      expect(order).toEqual(['kill', 'close', 'quit'])
      expect(kill).toHaveBeenCalledWith(-42, 'SIGTERM')
      expect(kill).toHaveBeenCalledWith(-42, 'SIGKILL')
    } finally { vi.useRealTimers() }
  })

  it('does not quit if process-group cleanup fails', async () => {
    let beforeQuit!: (ev: { preventDefault(): void }) => void
    const quit = vi.fn()
    const log = vi.fn()
    installPlaceQuit({ on: (_ev: unknown, cb: any) => { beforeQuit = cb }, quit } as any,
      { showMessageBox: async () => ({ response: 1 }) } as any,
      { active: () => [{ runId: 7, step: 'Example' }], stopAll: async () => { throw new Error('still alive') } }, vi.fn(), log)
    beforeQuit({ preventDefault() {} })
    await new Promise((r) => setTimeout(r, 0))
    expect(quit).not.toHaveBeenCalled()
    expect(log).toHaveBeenCalledWith(expect.stringContaining('still alive'))
  })
})
