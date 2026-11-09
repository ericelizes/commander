import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ChatHostContext } from '../host'
import { copyableMarkdown, Markdown } from '../Markdown'

afterEach(cleanup)

const graph = '{"title":"Plan","nodes":[{"id":"a","title":"Spike","state":"done"},{"id":"b","title":"Go live","change":"add"}]}'

function show(text: string, streaming: boolean, host = { openBead: vi.fn(), Graph: ({ spec }: { spec: unknown }) => <div data-testid="graph">{(spec as { title: string }).title}</div> }) {
  render(
    <ChatHostContext.Provider value={host}>
      <Markdown text={text} streaming={streaming} fadeAfter={streaming ? 0 : Infinity} />
    </ChatHostContext.Provider>,
  )
  return host
}

describe('agent links and graphs in chat', () => {
  it('a cmdr: link opens the bead, on the tab it names', () => {
    const host = show('See [the Manager](cmdr:example-app-t03) and [its plan](cmdr:example-app-t03.2/workflow).', false)
    fireEvent.click(screen.getByRole('button', { name: /the\s*Manager/ }))
    expect(host.openBead).toHaveBeenCalledWith('example-app-t03', undefined)
    fireEvent.click(screen.getByRole('button', { name: /its\s*plan/ }))
    expect(host.openBead).toHaveBeenLastCalledWith('example-app-t03.2', 'workflow')
  })

  it('a page link with a bead context opens there, and its address stays just the page', () => {
    const openBeadPage = vi.fn(() => true)
    const openUrl = vi.fn(() => true)
    show('See [PR 12](https://github.example/o/r/pull/12 "cmdr:example-app-t03.2").', false, { openBead: vi.fn(), openBeadPage, openUrl } as never)
    const link = screen.getByRole('link', { name: /PR\s*12/ })
    expect(link).toHaveAttribute('href', 'https://github.example/o/r/pull/12')
    expect(link).not.toHaveAttribute('title')
    fireEvent.click(link)
    expect(openBeadPage).toHaveBeenCalledWith('example-app-t03.2', 'https://github.example/o/r/pull/12', 'PR 12')
    expect(openUrl).not.toHaveBeenCalled()
  })

  it('copying a message leaves only the page in its links', () => {
    expect(copyableMarkdown('[PR](https://x.example/1 "cmdr:a-1.2") and [other](https://x.example/2 "A title")')).toBe('[PR](https://x.example/1) and [other](https://x.example/2 "A title")')
  })

  it('a graph block is a loading skeleton while its JSON streams in', () => {
    show('Here:\n\n```cmdr-graph\n' + graph.slice(0, 40), true)
    expect(screen.getByRole('status')).toHaveTextContent('Loading graph')
    expect(screen.queryByTestId('graph')).toBeNull()
  })

  it('a complete graph block becomes the graph', () => {
    show('Here:\n\n```cmdr-graph\n' + graph + '\n```\n', false)
    expect(screen.getByTestId('graph')).toHaveTextContent('Plan')
  })

  it('a finished block that is not valid JSON stays readable as code', () => {
    show('```cmdr-graph\n{"nodes": [\n```\n', false)
    expect(screen.queryByTestId('graph')).toBeNull()
    expect(screen.getByText(/"nodes"/)).toBeInTheDocument()
  })
})
