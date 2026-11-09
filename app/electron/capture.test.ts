import { describe, expect, it, vi } from 'vitest'
import { capturePng } from './capture'

describe('capturePng', () => {
  it('returns the window as a base64 PNG', async () => {
    const wc = { capturePage: vi.fn(async () => ({ isEmpty: () => false, toPNG: () => Buffer.from('PNG!') })) }
    expect(await capturePng(wc as never)).toBe(Buffer.from('PNG!').toString('base64'))
    expect(wc.capturePage).toHaveBeenCalledWith(undefined, { stayHidden: true })
  })

  it('refuses an empty image', async () => {
    const wc = { capturePage: vi.fn(async () => ({ isEmpty: () => true, toPNG: () => Buffer.alloc(0) })) }
    await expect(capturePng(wc as never)).rejects.toThrow(/rendered nothing/)
  })
})
