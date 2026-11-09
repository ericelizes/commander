import { describe, expect, it } from 'vitest'
import { fmtTokens } from '../src/renderer/src/components/UsageTab'

describe('fmtTokens', () => {
  it('reads at a glance from units to billions', () => {
    expect(fmtTokens(950)).toBe('950')
    expect(fmtTokens(12_400)).toBe('12k')
    expect(fmtTokens(3_400_000)).toBe('3.4M')
    expect(fmtTokens(473_000_000)).toBe('473M')
    expect(fmtTokens(3_143_000_000)).toBe('3.1B')
    expect(fmtTokens(14_700_000_000)).toBe('15B')
  })
})
