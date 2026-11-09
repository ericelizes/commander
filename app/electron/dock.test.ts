import { describe, expect, it, vi } from 'vitest'
import { badgeText, dockMenuTemplate, setNeedsYouBadge } from './dock'

describe('badgeText', () => {
  it('blank at zero, caps at 99+', () => {
    expect(badgeText(0)).toBe('')
    expect(badgeText(-1)).toBe('')
    expect(badgeText(Number.NaN)).toBe('')
    expect(badgeText(3)).toBe('3')
    expect(badgeText(100)).toBe('99+')
  })
})

describe('setNeedsYouBadge', () => {
  it('macOS sets the dock badge', () => {
    const app = { dock: { setBadge: vi.fn() }, setBadgeCount: vi.fn() }
    setNeedsYouBadge(app as never, 'darwin', 4)
    expect(app.dock.setBadge).toHaveBeenCalledWith('4')
    expect(app.setBadgeCount).not.toHaveBeenCalled()
  })

  it('Linux uses the launcher badge count and never touches dock', () => {
    const app = { setBadgeCount: vi.fn() } // app.dock is undefined on Linux
    setNeedsYouBadge(app as never, 'linux', 2)
    expect(app.setBadgeCount).toHaveBeenCalledWith(2)
  })
})

describe('dockMenuTemplate', () => {
  it('first 5 workstreams, click opens by id', () => {
    const open = vi.fn()
    const list = Array.from({ length: 7 }, (_, i) => ({ id: `ws${i}`, name: `Workstream ${i}` }))
    const items = dockMenuTemplate(list, open)
    expect(items.map((i) => i.label)).toEqual(['Workstream 0', 'Workstream 1', 'Workstream 2', 'Workstream 3', 'Workstream 4'])
    ;(items[2].click as () => void)()
    expect(open).toHaveBeenCalledWith('ws2')
  })
})
