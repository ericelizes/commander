import { describe, expect, it } from 'vitest'
import { classifyUserText } from '../events'

describe('compaction summary', () => {
  it('is the system’s, whatever origin its transcript line carried', () => {
    const text = 'This session is being continued from a previous conversation that ran out of context.'
    expect(classifyUserText(text, 'replay')).toBe('system')
    expect(classifyUserText(text)).toBe('system')
    expect(classifyUserText('hello', 'replay')).toBe('replay')
  })
})
