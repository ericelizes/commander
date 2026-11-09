import { afterEach, describe, expect, it } from 'vitest'
import { resolveClocks } from '../src/renderer/src/viewmodel'

// 23:04 UTC on 2026-10-28 is 7:04 PM in New York (EDT, UTC-4).
const at = Date.UTC(2026, 9, 28, 23, 4) / 1000
const orig = process.env.TZ

describe('board clock marks', () => {
  afterEach(() => {
    process.env.TZ = orig
  })

  it('reads in the viewer zone, not UTC', () => {
    process.env.TZ = 'America/New_York'
    const now = new Date((at - 25 * 60) * 1000)
    expect(resolveClocks(`Waiting for reconcile · @${at}`, now)).toBe('Waiting for reconcile · 07:04 PM')
  })

  it('adds the weekday on another day and leaves other text alone', () => {
    process.env.TZ = 'America/New_York'
    const now = new Date((at - 86400) * 1000)
    expect(resolveClocks(`Parked until @${at}`, now)).toBe('Parked until Wed 07:04 PM')
    expect(resolveClocks('mail me@example.com #123', now)).toBe('mail me@example.com #123')
  })
})
