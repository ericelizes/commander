import { describe, expect, it } from 'vitest'
import { homeActive } from '../src/renderer/src/viewmodel'

describe('homeActive', () => {
  it('is on while Commander is the conversation', () => {
    expect(homeActive({ kind: 'commander' })).toBe(true)
    expect(homeActive({ kind: 'ws', key: 'p/a' })).toBe(true)
    expect(homeActive({ kind: 'ask', key: 'ask:x' })).toBe(true)
  })
  it('is off on Progress and an agent chat', () => {
    expect(homeActive({ kind: 'progress' })).toBe(false)
    expect(homeActive({ kind: 'agent', key: 'manager-a', ws: 'p/a' })).toBe(false)
  })
})
