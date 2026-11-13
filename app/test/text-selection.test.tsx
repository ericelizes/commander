import { cleanup, fireEvent, render } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FlowGraph } from '../src/renderer/src/components/FlowGraph'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { useStore } from '../src/renderer/src/store'
import type { Snapshot } from '../src/shared/snapshot'

// Load the actual selection rules without jsdom's unsupported theme/layout CSS.
const css = readFileSync(join(__dirname, '../src/renderer/src/styles.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
const selectionCSS = [...css.matchAll(/([^{}]+)\{[^{}]*?\buser-select:\s*([^;}]+)[^{}]*\}/g)]
  .map((m) => `${m[1]} { user-select: ${m[2]}; }`).join('\n')
let style: HTMLStyleElement

beforeEach(() => {
  style = document.createElement('style')
  style.textContent = selectionCSS
  document.head.append(style)
})
afterEach(() => {
  cleanup()
  style.remove()
  document.body.className = ''
  vi.unstubAllGlobals()
  localStorage.clear()
  useStore.setState({ snapshot: null, config: null })
})

describe('text selection', () => {
  it('defaults readable content to selectable text', () => {
    expect(getComputedStyle(document.body).userSelect).toBe('text')
  })

  it('keeps controls inert and content-bearing buttons selectable', () => {
    const { container } = render(<>
      <button>Send</button><select /><summary>Expand</summary>
      <div role="button" className="avatar">Agent</div><div role="tab">Inspector</div><div role="separator" />
      <span className="ws-ring">1</span><span className="track-rank">1.1</span>
      {['row', 'op', 'sp-card', 'sp-item-title', 'sp-item-ws', 'canvas-box-title', 'flow-block-title', 'stage-row', 'flow-step', 'nav-move-error'].map((name) => <button key={name} className={name}>Readable content</button>)}
    </>)
    for (const el of container.children) {
      expect(getComputedStyle(el).userSelect, el.outerHTML).toBe(el.textContent === 'Readable content' ? 'text' : 'none')
    }
  })

  it.each(['resizing', 'nav-dragging'])('suppresses selection during %s', (name) => {
    const { container } = render(<button className="row">Readable content</button>)
    document.body.classList.add(name)
    expect(getComputedStyle(container.firstElementChild!).userSelect).toBe('none')
  })


  it('leaves diagram text selectable while the canvas background still pans', () => {
    const snapshot = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
    useStore.setState({ snapshot, selected: { kind: 'commander' } })
    const { container } = render(<FlowGraph />)
    const canvas = container.querySelector<HTMLElement>('.flow-canvas')!
    canvas.setPointerCapture = vi.fn()
    fireEvent.pointerDown(container.querySelector('.flow-state')!)
    expect(canvas).not.toHaveClass('panning')
    expect(canvas.setPointerCapture).not.toHaveBeenCalled()
    fireEvent.pointerDown(canvas)
    expect(canvas).toHaveClass('panning')
    expect(canvas.setPointerCapture).toHaveBeenCalledOnce()
    fireEvent.pointerUp(canvas)
    expect(canvas).not.toHaveClass('panning')
  })

  it('selects sidebar text without lifting the row, and still drags from its rank', () => {
    const snapshot = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 404 })))
    vi.stubGlobal('CSS', { escape: (s: string) => s })
    useStore.setState({ snapshot, config: { apiUrl: 'http://127.0.0.1:9', token: 'test' }, completed: {}, leaving: {} })
    const { container } = render(<Navigator />)
    const row = container.querySelector('[data-nav-key]')!
    const press = (el: Element) => fireEvent(el, new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 10, clientY: 10 }))
    const move = () => fireEvent(window, new MouseEvent('pointermove', { clientX: 30, clientY: 30 }))
    for (const selector of ['.row-title', '.ws-state-text']) {
      press(row.querySelector(selector)!)
      move()
      expect(document.body).not.toHaveClass('nav-dragging')
      fireEvent(window, new MouseEvent('pointerup'))
    }
    press(row.querySelector('.ws-ring')!)
    move()
    expect(document.body).toHaveClass('nav-dragging')
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(document.body).not.toHaveClass('nav-dragging')
  })
})
