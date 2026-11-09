import { describe, expect, it, vi } from 'vitest'
import { capturePage, MAX_TEXT, MAX_WIDTH, type CaptureSource } from './capture'

const img = (width: number) => {
  const i = { isEmpty: () => false, getSize: () => ({ width, height: 10 }), resize: vi.fn((o: { width: number }) => img(o.width)), toPNG: () => Buffer.from(`w${width}`) }
  return i
}
const src = (url: string, width = 2000, text: unknown = 'hello'): CaptureSource => ({
  getURL: () => url,
  getTitle: () => 'T',
  capturePage: async () => img(width),
  executeJavaScript: async () => text,
})

describe('capturePage', () => {
  it('downscales to 1280 and returns the text', async () => {
    const r = await capturePage(src('https://a.test/x'), true, [])
    expect(r).toMatchObject({ ok: true, capture: { url: 'https://a.test/x', title: '', text: 'hello' } })
    if (r.ok) expect(Buffer.from(r.capture.png, 'base64').toString()).toBe(`w${MAX_WIDTH}`)
  })
  it('caps the text', async () => {
    const r = await capturePage(src('https://a.test/', 800, 'x'.repeat(MAX_TEXT + 5)), true, [])
    expect(r.ok && r.capture.text.length).toBe(MAX_TEXT)
  })
  it('never reads a view that is not shown', async () => {
    const wc = src('https://a.test/')
    const spy = vi.spyOn(wc, 'capturePage')
    expect(await capturePage(wc, false, [])).toEqual({ ok: false, reason: 'not-shown' })
    expect(spy).not.toHaveBeenCalled()
  })
  it('refuses a blocked url without capturing', async () => {
    const wc = src('https://app.terraform.io/x')
    const spy = vi.spyOn(wc, 'capturePage')
    expect(await capturePage(wc, true, ['*.terraform.io'])).toEqual({ ok: false, reason: 'blocked' })
    expect(spy).not.toHaveBeenCalled()
  })
  it('sends origin and path only, never the query, fragment or title', async () => {
    const r = await capturePage(src('https://a.test/x?id=7#/p'), true, [])
    expect(r.ok && r.capture.url).toBe('https://a.test/x')
    expect(r.ok && r.capture.title).toBe('')
  })
  it('refuses when the tab is no longer the page the user saw', async () => {
    expect(await capturePage(src('https://a.test/b'), true, [], 'https://a.test/a')).toEqual({ ok: false, reason: 'failed' })
    expect((await capturePage(src('https://a.test/a?q=1'), true, [], 'https://a.test/a')).ok).toBe(true)
  })
  it('refuses when the page moved to another one during the capture', async () => {
    let n = 0
    const wc = { ...src('https://a.test/a'), getURL: () => (n++ === 0 ? 'https://a.test/a' : 'https://a.test/b') }
    expect(await capturePage(wc, true, [])).toEqual({ ok: false, reason: 'failed' })
  })
  it('refuses a move to a blocked page during the capture', async () => {
    let n = 0
    const wc = { ...src('https://a.test/a'), getURL: () => (n++ === 0 ? 'https://a.test/a' : 'https://private.test/') }
    expect(await capturePage(wc, true, ['private.test'])).toEqual({ ok: false, reason: 'blocked' })
  })
  it('fails closed when the capture throws', async () => {
    const wc = { ...src('https://a.test/'), capturePage: async () => { throw new Error('x') } }
    expect(await capturePage(wc, true, [])).toEqual({ ok: false, reason: 'failed' })
  })
})
