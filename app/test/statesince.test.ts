import { beforeEach, describe, expect, it } from 'vitest'
import { resetTracked, trackedSince } from '../src/renderer/src/stateSince'

describe('trackedSince', () => {
  beforeEach(() => resetTracked())
  it('keeps the start while the state holds, restarts when it changes', () => {
    expect(trackedSince('w', 'working|Waiting for CI', 100)).toBe(100)
    expect(trackedSince('w', 'working|Waiting for CI', 4000)).toBe(100)
    expect(trackedSince('w', 'waiting|Waiting for review', 4100)).toBe(4100)
    expect(trackedSince('other', 'stalled|Stalled', 4200)).toBe(4200)
  })
})
