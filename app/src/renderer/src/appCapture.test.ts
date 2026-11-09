import { describe, expect, it } from 'vitest'
import { answerCapture, visibleText } from './appCapture'

describe('answerCapture', () => {
  const base = { domPng: async () => 'DOM', text: () => 'hello' }

  it('prefers Electron capturePage', async () => {
    const r = await answerCapture('7', { ...base, electronPng: async () => 'ELEC' })
    expect(r).toEqual({ type: 'capture', id: '7', png: 'ELEC', text: 'hello', source: 'electron-capturePage' })
  })

  it('renders the DOM when there is no Electron capture', async () => {
    const r = await answerCapture('1', base)
    expect(r).toMatchObject({ png: 'DOM', source: 'dom', text: 'hello' })
    expect(r.error).toBeUndefined()
  })

  it('falls back to the DOM when capturePage fails, and says so', async () => {
    const r = await answerCapture('1', { ...base, electronPng: async () => { throw new Error('hidden') } })
    expect(r.source).toBe('dom')
    expect(r.error).toContain('capturePage: Error: hidden')
  })

  it('keeps the text when no picture can be made', async () => {
    const r = await answerCapture('1', { ...base, domPng: async () => { throw new Error('tainted') } })
    expect(r.png).toBeUndefined()
    expect(r.text).toBe('hello')
    expect(r.error).toContain('tainted')
  })
})

describe('visibleText', () => {
  it('collapses blank runs and truncates', () => {
    const doc = { body: { innerText: `a\n\n\n\nb${'x'.repeat(40_000)}` } } as unknown as Document
    const t = visibleText(doc)
    expect(t.startsWith('a\n\nb')).toBe(true)
    expect(t.endsWith('(truncated)')).toBe(true)
  })
})
