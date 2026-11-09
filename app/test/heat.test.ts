import { describe, expect, it } from 'vitest'
import { doneSince, heatOf } from '../src/renderer/src/heat'

describe('heat', () => {
  it('rises with agents working, caps at an ember without progress, drops a level per death', () => {
    expect(heatOf(0, 5, 0).level).toBe(0)
    expect(heatOf(2, 5, 0).level).toBe(1)
    expect(heatOf(6, 5, 0).level).toBe(2)
    expect(heatOf(12, 5, 0).level).toBe(3)
    expect(heatOf(12, null, 0).level).toBe(3)
    expect(heatOf(12, 0, 0).level).toBe(1)
    expect(heatOf(12, 5, 2).level).toBe(1)
  })

  it('counts steps done since the newest sample at least 30 minutes old', () => {
    const now = 10_000
    expect(doneSince([[now - 600, 40]], now, 45)).toBeNull()
    expect(doneSince([[now - 2400, 30], [now - 1900, 35], [now - 600, 40]], now, 45)).toBe(10)
  })
})
