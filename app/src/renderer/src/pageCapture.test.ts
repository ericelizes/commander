// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CAPTURE_TIMEOUT_MS, invalidBlockLines, preview, setBlockText, setCaptureEnabled, setWeb, snapshot, take } from './pageCapture'

const ok = { ok: true as const, capture: { url: 'https://app.terraform.io/runs', title: '', png: 'AAAA', text: 'plan failed' } }
const setBridge = (capture?: (r: { id: number; block: string[]; expect?: string }) => Promise<unknown>) => {
  ;(window as unknown as { commanderHost?: unknown }).commanderHost = capture ? { webview: { capture } } : undefined
}

beforeEach(() => {
  localStorage.clear()
  setBlockText('')
  setCaptureEnabled(true)
  setWeb({ id: 3, url: 'https://app.terraform.io/runs?id=7', title: 'Runs' })
})
afterEach(() => {
  setBridge()
  setWeb(null)
  localStorage.clear()
  vi.useRealTimers()
})

describe('pageCapture', () => {
  it('is off by default: nothing is previewed, snapshotted or requested', async () => {
    localStorage.clear()
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    expect(preview('commander')?.off ?? true).toBe(true)
    expect(snapshot('commander')).toBeNull()
    expect(await take('commander')).toBeNull()
    expect(cap).not.toHaveBeenCalled()
  })

  it('attaches a screenshot and the page text, with a note of origin and path only', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    const r = await take('commander')
    expect(cap).toHaveBeenCalledWith({ id: 3, block: [], expect: 'https://app.terraform.io/runs' })
    expect(r?.attachments.map((a) => a.name.replace(/\d+/, 'N'))).toEqual(['capture-N.png', 'capture-N.txt'])
    expect(r?.attachments[1].dataUrl).toBe(`data:text/plain;base64,${btoa('plan failed')}`)
    expect(r?.note).toContain('(https://app.terraform.io/runs)')
    expect(r?.note).not.toContain('id=7')
    expect(r?.note).not.toContain('Runs')
  })

  it('never captures for an Operator, without Electron, or without a web tab', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    expect(await take('operator-x')).toBeNull()
    setWeb(null)
    expect(await take('manager-x')).toBeNull()
    setWeb({ id: 3, url: 'https://a.test/', title: 'A' })
    setBridge()
    expect(await take('commander')).toBeNull()
    expect(cap).not.toHaveBeenCalled()
  })

  it('a blocked page is not requested at all', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    setBlockText('*.terraform.io')
    expect(preview('commander')?.blocked).toBe(true)
    expect(await take('commander')).toBeNull()
    expect(cap).not.toHaveBeenCalled()
  })

  it('a pasted URL in the block list blocks, and a line it cannot read is flagged and blocks', () => {
    setBridge(async () => ok)
    setBlockText('https://app.terraform.io/runs')
    expect(preview('commander')?.blocked).toBe(true)
    setBlockText('app terraform')
    expect(invalidBlockLines()).toEqual(['app terraform'])
    expect(preview('commander')?.blocked).toBe(true)
  })

  it('the decision is fixed at the press: a later tab or a block-list change does not change what is captured', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    const target = snapshot('commander')
    setWeb({ id: 9, url: 'https://private.test/record', title: 'Record' })
    await take('commander', target)
    expect(cap).toHaveBeenCalledWith({ id: 3, block: [], expect: 'https://app.terraform.io/runs' })
  })

  it('a message sent while the tab was blocked stays uncaptured when drained after the tab moved on', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    setBlockText('terraform.io')
    const target = snapshot('commander')
    expect(target).toBeNull()
    setBlockText('')
    expect(await take('commander', target)).toBeNull()
    expect(cap).not.toHaveBeenCalled()
  })

  it('turning capture off before a held message drains attaches nothing', async () => {
    const cap = vi.fn(async () => ok)
    setBridge(cap)
    const target = snapshot('commander')
    setCaptureEnabled(false)
    expect(await take('commander', target)).toBeNull()
    expect(cap).not.toHaveBeenCalled()
  })

  it('a slow capture is dropped after 2s so the message is not delayed', async () => {
    vi.useFakeTimers()
    setBridge(() => new Promise(() => {}))
    const p = take('commander')
    await vi.advanceTimersByTimeAsync(CAPTURE_TIMEOUT_MS + 1)
    expect(await p).toBeNull()
  })

  it('a refusal or error from main attaches nothing', async () => {
    setBridge(async () => ({ ok: false, reason: 'not-shown' }))
    expect(await take('commander')).toBeNull()
    setBridge(async () => {
      throw new Error('x')
    })
    expect(await take('commander')).toBeNull()
  })
})
