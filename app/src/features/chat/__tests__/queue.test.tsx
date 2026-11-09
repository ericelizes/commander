// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { Composer, type ComposerProps } from '../Composer'

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const base: Omit<ComposerProps, 'waiting'> = { role: 'op-queue', running: true, commands: [], onSend: () => {}, onStop: () => {} }

// jsdom has no layout: give the queue a fixed box and a content height per item.
function sized(list: HTMLElement) {
  Object.defineProperty(list, 'clientHeight', { configurable: true, get: () => 120 })
  Object.defineProperty(list, 'scrollHeight', { configurable: true, get: () => list.children.length * 34 })
}

const items = (n: number, from = 1) => Array.from({ length: n }, (_, i) => `queued ${i + from}`)

function send(text: string) {
  fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: text } })
  fireEvent.click(screen.getByLabelText('Send'))
}

function scrollUp(list: HTMLElement) {
  list.scrollTop = 0
  fireEvent.scroll(list)
}

describe('queued sends', () => {
  it('scrolls the bounded queue to the newest send', () => {
    const { rerender } = render(<Composer {...base} waiting={items(3)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: 'queued 4' } })
    fireEvent.click(screen.getByLabelText('Send'))
    rerender(<Composer {...base} waiting={items(4)} />)
    expect(list.scrollTop).toBe(4 * 34)
    rerender(<Composer {...base} waiting={items(5)} />) // still at the end: keep following
    expect(list.scrollTop).toBe(5 * 34)
  })

  it('leaves a queue you scrolled up alone when a send arrives from elsewhere', () => {
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    list.scrollTop = 0
    fireEvent.scroll(list)
    rerender(<Composer {...base} waiting={items(6)} />)
    expect(list.scrollTop).toBe(0)
    rerender(<Composer {...base} waiting={items(5)} />) // one was read: no move either
    expect(list.scrollTop).toBe(0)
  })

  it('shows your own send even while you read older ones', () => {
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    list.scrollTop = 0
    fireEvent.scroll(list)
    fireEvent.change(screen.getByRole('textbox', { name: 'Message' }), { target: { value: 'queued 6' } })
    fireEvent.click(screen.getByLabelText('Send'))
    rerender(<Composer {...base} waiting={items(6)} />)
    expect(list.scrollTop).toBe(6 * 34)
  })

  it('follows again after the queue emptied while you were scrolled up', () => {
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    sized(screen.getByRole('list', { name: 'Sent, not read yet' }))
    scrollUp(screen.getByRole('list', { name: 'Sent, not read yet' }))
    rerender(<Composer {...base} waiting={[]} />) // all read: the queue goes away
    rerender(<Composer {...base} waiting={items(1, 10)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    rerender(<Composer {...base} waiting={items(5, 10)} />) // sends from elsewhere
    expect(list.scrollTop).toBe(5 * 34)
  })

  it('shows your send when the oldest is read in the same update', () => {
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    scrollUp(list)
    send('queued 6')
    rerender(<Composer {...base} waiting={items(5, 2)} />) // 1 read, 6 queued: same length
    expect(list.scrollTop).toBe(5 * 34)
    scrollUp(list)
    rerender(<Composer {...base} waiting={items(5, 3)} />) // the next one is not yours
    expect(list.scrollTop).toBe(0)
  })

  it('leaves a queue you scrolled up alone when a read and a send from elsewhere land together', () => {
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    scrollUp(list)
    rerender(<Composer {...base} waiting={items(5, 2)} />)
    expect(list.scrollTop).toBe(0)
  })

  it('forgets a send of yours that never showed in the queue', () => {
    const now = vi.spyOn(Date, 'now').mockReturnValue(1_000_000)
    const { rerender } = render(<Composer {...base} waiting={items(5)} />)
    const list = screen.getByRole('list', { name: 'Sent, not read yet' })
    sized(list)
    send('read at once') // the agent took it straight away
    scrollUp(list)
    now.mockReturnValue(1_000_000 + 60_000)
    rerender(<Composer {...base} waiting={items(6)} />) // a minute later, from elsewhere
    expect(list.scrollTop).toBe(0)
  })
})
