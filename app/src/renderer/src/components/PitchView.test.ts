import { describe, expect, it } from 'vitest'
import { pitchUnwritten } from './PitchView'

describe('pitchUnwritten', () => {
  it('is a card with no write-up, options or steps', () => {
    expect(pitchUnwritten(undefined, '')).toBe(true)
    expect(pitchUnwritten({ question: 'Greenlight merging the fix?' }, undefined)).toBe(true)
  })

  it('is never a card the daemon lists in Needs you', () => {
    expect(pitchUnwritten(undefined, 'Stages: build, ship.')).toBe(false)
    expect(pitchUnwritten({ question: 'Pick one', options: [{ label: 'A' }] }, '')).toBe(false)
    expect(pitchUnwritten({ question: 'Run it', steps: ['cmdr x'] }, '')).toBe(false)
    expect(pitchUnwritten({ question: 'Ship?', context: 'Two lines.' }, '')).toBe(false)
  })
})
