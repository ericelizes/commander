// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, describe, expect, it } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { Rail } from '../src/renderer/src/components/Rail'
import { TopBar } from '../src/renderer/src/components/TopBar'
import { useStore } from '../src/renderer/src/store'

afterEach(() => {
  cleanup()
  useStore.setState({ selected: { kind: 'commander' }, focus: null, narrow: false, chatTab: true })
})

describe('the top bar', () => {
  it('names the chat alone with nothing in focus', () => {
    render(<TopBar left={397} chat={640} />)
    expect(screen.getByText('Commander')).toBeInTheDocument()
    expect(screen.queryByRole('heading')).toBeNull()
    expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument()
  })

  it('names the focus and the chat over their panes, and Home leaves focus', () => {
    useStore.setState({ selected: { kind: 'ws', key: 'p/w' }, focus: { n: 1, title: 'Data Entry Hub' } })
    render(<><TopBar left={397} chat={640} /><Rail /></>)
    expect(screen.getByRole('heading')).toHaveTextContent('Workstream 1: Data Entry Hub')
    expect(screen.getByText('Commander')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Home' }))
    expect(useStore.getState().selected).toEqual({ kind: 'commander' })
  })

  it('Progress is a page: the bar names it, the rail goes there, and Back returns', () => {
    render(<><TopBar left={397} chat={640} /><Rail /></>)
    fireEvent.click(screen.getByRole('button', { name: 'Progress' }))
    expect(useStore.getState().selected).toEqual({ kind: 'progress' })
    expect(screen.getByText('Progress', { selector: '.app-top-title' })).toBeInTheDocument()
    expect(screen.queryByText('Commander')).toBeNull()
    act(() => useStore.getState().goBack())
    expect(useStore.getState().selected).toEqual({ kind: 'commander' })
  })

  it('names only the pane on screen in a narrow window', () => {
    useStore.setState({ selected: { kind: 'ws', key: 'p/w' }, focus: { n: 1, title: 'Data Entry Hub' }, narrow: true, chatTab: false })
    render(<TopBar left={397} chat={640} />)
    expect(screen.getByRole('heading')).toBeInTheDocument()
    expect(screen.queryByText('Commander')).toBeNull()
  })
})
