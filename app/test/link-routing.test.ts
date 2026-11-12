// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'

const openInPanel = vi.fn()
vi.mock('../src/features/inspector', () => ({ hasConnections: () => true, openInPanel: (...a: unknown[]) => openInPanel(...a) }))
const { routeLinksToPanel } = await import('../src/renderer/src/linkRouting')

afterEach(() => { document.body.innerHTML = ''; openInPanel.mockReset() })

const click = (a: Element) => {
  const e = new MouseEvent('click', { bubbles: true, cancelable: true, button: 0 })
  a.dispatchEvent(e)
  return e
}

describe('web links', () => {
  it('open in the side panel outside a chat', () => {
    const off = routeLinksToPanel()
    document.body.innerHTML = '<div><a href="https://github.com/o/r/pull/1">PR</a></div>'
    expect(click(document.querySelector('a')!).defaultPrevented).toBe(true)
    expect(openInPanel).toHaveBeenCalled()
    off()
  })
  it('in a chat are left to the system browser', () => {
    const off = routeLinksToPanel()
    document.body.innerHTML = '<section class="cmdr-chat"><p><a href="https://github.com/o/r/pull/1" target="_blank">PR</a></p></section>'
    expect(click(document.querySelector('a')!).defaultPrevented).toBe(false)
    expect(openInPanel).not.toHaveBeenCalled()
    off()
  })
})
