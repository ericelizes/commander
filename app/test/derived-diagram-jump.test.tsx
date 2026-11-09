// @vitest-environment jsdom
import { act, cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Diagram } from '../src/shared/snapshot'
import { DerivedDiagram } from '../src/renderer/src/components/DerivedDiagram'

type Cb = (entries: Partial<IntersectionObserverEntry>[]) => void
let observers: { cb: Cb; target: Element | null; disconnected: boolean }[] = []

beforeEach(() => {
  observers = []
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      o = { cb: null as unknown as Cb, target: null as Element | null, disconnected: false }
      constructor(cb: Cb) {
        this.o.cb = cb
        observers.push(this.o)
      }
      observe(el: Element) {
        this.o.target = el
      }
      disconnect() {
        this.o.disconnected = true
      }
    },
  )
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const mk = (current: string): Diagram => ({
  id: 'w',
  title: 'W',
  current,
  steps: [
    { id: 'a', title: 'First', kind: 'working', text: 'Operator working', lane: 'agents', actor: 'operators' },
    { id: 'b', title: 'Second', kind: 'working', text: 'Operator working', lane: 'agents', actor: 'operators' },
  ],
})

const live = () => observers.filter((o) => !o.disconnected).at(-1)!
const fire = (isIntersecting: boolean, top: number) =>
  act(() => live().cb([{ isIntersecting, boundingClientRect: { top } as DOMRectReadOnly, rootBounds: { top: 100 } as DOMRectReadOnly }]))

describe('jump to the current step', () => {
  it('shows a down arrow when the current step is below, and click scrolls it into view', async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    render(<DerivedDiagram diagram={mk('b')} />)
    expect(screen.queryByRole('button', { name: 'Jump to the current step' })).toBeNull()
    fire(false, 900)
    const btn = screen.getByRole('button', { name: 'Jump to the current step' })
    expect(btn.title).toContain('below')
    scroll.mockClear()
    await userEvent.click(btn)
    expect(scroll).toHaveBeenCalledWith(expect.objectContaining({ block: 'center' }))
  })

  it('shows an up arrow when the current step is above', () => {
    render(<DerivedDiagram diagram={mk('b')} />)
    fire(false, -50)
    expect(screen.getByRole('button', { name: 'Jump to the current step' }).title).toContain('above')
  })

  it('hides once the step is visible', () => {
    render(<DerivedDiagram diagram={mk('b')} />)
    fire(false, 900)
    expect(screen.getByRole('button')).toBeTruthy()
    fire(true, 300)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('has no button with no current step', () => {
    render(<DerivedDiagram diagram={{ id: 'w', title: 'W', steps: [{ id: 'a', title: 'Done', kind: 'done', text: 'Done', lane: 'agents', actor: 'operators' }] }} />)
    expect(observers.filter((o) => o.target)).toHaveLength(0)
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('follows the current step when it changes', () => {
    const { rerender } = render(<DerivedDiagram diagram={mk('a')} />)
    expect(live().target?.textContent).toContain('First')
    rerender(<DerivedDiagram diagram={mk('b')} />)
    expect(observers[0].disconnected).toBe(true)
    expect(live().target?.textContent).toContain('Second')
  })
})
