// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ChatPane } from '../ChatPane'
import { ChatHostContext, type ChatHost } from '../host'
import { ReplayTransport } from '../transport'

afterEach(cleanup)

function mount(preview: { host: string; title: string; blocked: boolean } | null, take = vi.fn(async () => ({ attachments: [{ name: 'capture-1.png', mediaType: 'image/png', dataUrl: 'data:image/png;base64,AAAA' }], note: '- Focused browser tab' }))) {
  const snapshot = vi.fn(() => ({ id: 1 }))
  const transport = new ReplayTransport([], { echo: true })
  const host: ChatHost = { screen: () => '- Right panel: a web tab', capture: { usePreview: () => preview, snapshot, take } }
  render(
    <ChatHostContext.Provider value={host}>
      <ChatPane role="commander" transport={transport} initialRect={{ width: 900, height: 600 }} />
    </ChatHostContext.Provider>,
  )
  return { transport, take, snapshot }
}

const type = (t: string) => fireEvent.change(screen.getByLabelText('Message'), { target: { value: t } })
const req = (t: ReplayTransport) => (t.calls[0] as { req: { text: string; attachments?: { name: string }[] } }).req

describe('page capture in the composer', () => {
  it('shows what will be attached and sends the capture with the note', async () => {
    const { transport } = mount({ host: 'app.terraform.io', title: 'Runs', blocked: false })
    expect(document.querySelector('.cc-share')).toHaveTextContent('Sharing your window')
    type('why is this failing?')
    fireEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(transport.calls).toHaveLength(1))
    expect(req(transport).attachments?.map((a) => a.name)).toEqual(['capture-1.png'])
    expect(req(transport).text).toContain('- Focused browser tab')
  })

  it('a blocked page says so and offers no opt-in', () => {
    mount({ host: 'private.example.com', title: 'Record', blocked: true })
    expect(document.querySelector('.cc-share')).toHaveTextContent('private.example.com not shared')
  })

  it('no web tab: no indicator, and the message still goes', async () => {
    const { transport } = mount(null, vi.fn(async () => null) as never)
    expect(document.querySelector('.cc-share')).toBeNull()
    type('hi')
    fireEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(transport.calls).toHaveLength(1))
    expect(req(transport).attachments).toBeUndefined()
  })

  it('the capture is decided at the press and handed to take, even when the send is held', async () => {
    const { transport, take, snapshot } = mount({ host: 'app.terraform.io', title: 'Runs', blocked: false })
    type('hi')
    fireEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(transport.calls).toHaveLength(1))
    expect(snapshot).toHaveBeenCalledTimes(1)
    expect(take).toHaveBeenCalledWith('commander', { id: 1 })
  })

  it('a press with nothing to capture never asks take, whatever shows later', async () => {
    const { transport, take, snapshot } = mount({ host: 'app.terraform.io', title: 'Runs', blocked: false })
    snapshot.mockReturnValue(null as never)
    type('hi')
    fireEvent.click(screen.getByLabelText('Send'))
    await waitFor(() => expect(transport.calls).toHaveLength(1))
    expect(take).not.toHaveBeenCalled()
  })
})
