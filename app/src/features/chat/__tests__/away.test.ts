import { describe, expect, it } from 'vitest'
import { foldAway } from '../thread/toThread'

const mail = (id: string) => ({ type: 'data-item', id, data: { type: 'system', variant: 'mail', text: 'mail' } })
const text = (t: string) => ({ type: 'text', text: t })
const activity = { type: 'data-activity', id: 'a', data: { steps: [] } }

type P = Parameters<typeof foldAway>[0]

describe('foldAway', () => {
  it('leaves a reply with no mail alone', () => {
    const parts = [text('hi')] as unknown as P
    expect(foldAway(parts, 'r', false)).toBe(parts)
  })

  it('folds everything from the first mail and makes the last reply its report', () => {
    const parts = [text('answer to you'), mail('m1'), activity, text('update 1'), mail('m2'), text('report')] as unknown as P
    const out = foldAway(parts, 'r', false) as unknown as { type: string; text?: string; data?: { mails: number; report?: string; parts: unknown[] } }[]
    expect(out.map((p) => p.type)).toEqual(['text', 'data-away'])
    expect(out[0].text).toBe('answer to you')
    expect(out[1].data).toMatchObject({ mails: 2, report: 'report' })
    expect(out[1].data!.parts).toHaveLength(4)
  })

  it('keeps the last finished report while a new turn is still being written', () => {
    const parts = [mail('m1'), text('old report'), mail('m2'), text('new rep')] as unknown as P
    const out = foldAway(parts, 'r', true) as unknown as { data?: { report?: string; parts: unknown[] } }[]
    expect(out[0].data!.report).toBe('old report')
    expect(out[0].data!.parts).toHaveLength(3)
  })
})
