import { describe, expect, it } from 'vitest'
import { DEFAULT_WINDOW_STATE, parseWindowState } from './window-state'

const laptop = { x: 0, y: 25, width: 1512, height: 920 }
const external = { x: 1512, y: 0, width: 2560, height: 1415 }

describe('parseWindowState', () => {
  it('defaults on missing or corrupt data', () => {
    expect(parseWindowState(undefined, [laptop])).toEqual(DEFAULT_WINDOW_STATE)
    expect(parseWindowState('{nope', [laptop])).toEqual(DEFAULT_WINDOW_STATE)
  })

  it('restores bounds and maximized when on a display', () => {
    const raw = JSON.stringify({ x: 1600, y: 40, width: 1800, height: 1100, maximized: true })
    expect(parseWindowState(raw, [laptop, external])).toEqual({ x: 1600, y: 40, width: 1800, height: 1100, maximized: true })
  })

  it('drops a position that is off every display (monitor unplugged)', () => {
    const raw = JSON.stringify({ x: 1600, y: 40, width: 1800, height: 1100, maximized: false })
    const s = parseWindowState(raw, [laptop])
    expect(s.x).toBeUndefined()
    expect(s.y).toBeUndefined()
    expect(s.width).toBe(1800)
  })

  it('clamps tiny sizes and ignores junk types', () => {
    const s = parseWindowState(JSON.stringify({ width: 10, height: 'x', maximized: 'yes' }), [laptop])
    expect(s).toEqual({ width: 800, height: DEFAULT_WINDOW_STATE.height, maximized: false })
  })
})
