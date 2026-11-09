import { describe, expect, it, vi } from 'vitest'
import { ChatHostContext } from '../host'
import { fireEvent, render, screen } from '@testing-library/react'
import { Markdown } from '../Markdown'
import { openInPanel } from '../../inspector'

describe('Markdown when a reply finishes streaming', () => {
  it('keeps the rendered nodes: no remount, same structure', () => {
    const text = 'Hello **world** and more'
    const { container, rerender } = render(<Markdown text={text} streaming fadeAfter={0} />)
    const before = Array.from(container.querySelectorAll('span'))
    expect(before.length).toBeGreaterThan(0)
    rerender(<Markdown text={text} streaming={false} fadeAfter={Infinity} />)
    const after = Array.from(container.querySelectorAll('span'))
    expect(after).toHaveLength(before.length)
    after.forEach((node, i) => expect(node).toBe(before[i]))
  })
})

describe('web links', () => {
  it('open in the host side panel on a click; Cmd-click leaves them to the browser', () => {
    const opened: string[] = []
    render(
      <ChatHostContext.Provider value={{ openUrl: (u) => (opened.push(u), true) }}>
        <Markdown text="Go to [the device page](https://login.microsoft.com/device)." />
      </ChatHostContext.Provider>,
    )
    const link = screen.getByRole('link', { name: 'the device page' })
    expect(fireEvent.click(link)).toBe(false) // default prevented: no new window
    expect(opened).toEqual(['https://login.microsoft.com/device'])
    expect(fireEvent.click(link, { metaKey: true })).toBe(true)
    expect(opened).toHaveLength(1)
  })

  it('opens in the browser when no side panel is on screen (the Commander chat alone)', () => {
    const openExternal = vi.fn()
    ;(window as { commanderHost?: unknown }).commanderHost = { webview: { openExternal } }
    try {
      render(
        <ChatHostContext.Provider value={{ openUrl: (u) => (openInPanel(u), true) }}>
          <Markdown text="See [the PR](https://github.com/example-org/example-repo/pull/12)." />
        </ChatHostContext.Provider>,
      )
      fireEvent.click(screen.getByRole('link', { name: 'the PR' }))
      expect(openExternal).toHaveBeenCalledWith('https://github.com/example-org/example-repo/pull/12')
    } finally {
      delete (window as { commanderHost?: unknown }).commanderHost
    }
  })
})
