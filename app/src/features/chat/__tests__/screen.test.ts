import { describe, expect, it } from 'vitest'
import { stripScreen, withScreen } from '../screen'

describe('screen block', () => {
  it('goes ahead of the message and comes off again for the transcript', () => {
    const sent = withScreen('- Right panel: Graph tab', 'what is blocking the job workers?')
    expect(sent).toMatch(/^<user-screen>\n- Right panel: Graph tab\n<\/user-screen>\n\nwhat is blocking the job workers\?$/)
    expect(stripScreen(sent)).toBe('what is blocking the job workers?')
  })
  it('leaves a message without one alone', () => {
    expect(withScreen('', 'hi')).toBe('hi')
    expect(stripScreen('see <user-screen> in my text')).toBe('see <user-screen> in my text')
  })
})
