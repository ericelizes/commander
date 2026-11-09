// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ChatPane } from '../ChatPane'
import { ReplayTransport, WsChatTransport } from '../transport'

afterEach(cleanup)

const png = () => new File([new Uint8Array([137, 80, 78, 71])], 'shot.png', { type: 'image/png' })
const doc = () => new File(['hello'], 'notes.txt', { type: 'text/plain' })

function mount() {
  const transport = new ReplayTransport([], { echo: true })
  render(<ChatPane role="op-test" transport={transport} initialRect={{ width: 900, height: 600 }} />)
  return transport
}

describe('chat attachments', () => {
  it('paperclip picks files into chips, and Send carries them', async () => {
    const t = mount()
    const input = screen.getByLabelText('Choose files') as HTMLInputElement
    fireEvent.change(input, { target: { files: [png(), doc()] } })
    await screen.findByText('notes.txt')
    expect(screen.getByLabelText('Remove shot.png')).toBeInTheDocument()
    fireEvent.click(screen.getByLabelText('Send')) // files alone are a message
    await waitFor(() => expect(t.calls).toHaveLength(1))
    const req = (t.calls[0] as { req: { attachments: { name: string; mediaType: string; dataUrl: string }[] } }).req
    expect(req.attachments.map((a) => [a.name, a.mediaType])).toEqual([['shot.png', 'image/png'], ['notes.txt', 'text/plain']])
    expect(req.attachments[1].dataUrl).toBe('data:text/plain;base64,aGVsbG8=')
    // The sent bubble keeps the picture (a thumbnail) and names the other file.
    expect(await screen.findByAltText('shot.png')).toBeInTheDocument()
    expect(screen.getAllByText('notes.txt').length).toBeGreaterThan(0)
    expect(screen.queryByLabelText('Remove shot.png')).toBeNull() // composer cleared
  })

  it('a chip can be removed before sending', async () => {
    mount()
    fireEvent.change(screen.getByLabelText('Choose files'), { target: { files: [doc()] } })
    fireEvent.click(await screen.findByLabelText('Remove notes.txt'))
    expect(screen.queryByText('notes.txt')).toBeNull()
  })

  it('pasting a screenshot attaches it', async () => {
    mount()
    fireEvent.paste(screen.getByLabelText('Message'), { clipboardData: { files: [png()] } })
    expect(await screen.findByLabelText('Remove shot.png')).toBeInTheDocument()
  })

  it('dropping files anywhere on the chat attaches them', async () => {
    mount()
    const zone = screen.getByLabelText('Chat: op-test')
    fireEvent.dragEnter(zone, { dataTransfer: { types: ['Files'] } })
    expect(zone).toHaveClass('cc-dragging')
    fireEvent.drop(zone, { dataTransfer: { types: ['Files'], files: [doc()] } })
    expect(zone).not.toHaveClass('cc-dragging')
    expect(await screen.findByLabelText('Remove notes.txt')).toBeInTheDocument()
  })

  it('a file over the limit is refused with a reason', async () => {
    mount()
    const big = new File(['x'], 'huge.bin')
    Object.defineProperty(big, 'size', { value: 30 * 1024 * 1024 })
    fireEvent.change(screen.getByLabelText('Choose files'), { target: { files: [big] } })
    expect(await screen.findByRole('alert')).toHaveTextContent('huge.bin is over 25 MB')
  })
})

describe('WsChatTransport send', () => {
  it('posts attachments as {name, mime, data} with the data URL prefix stripped', async () => {
    const fetchImpl = vi.fn(async () => new Response('{"ok":true}')) as unknown as typeof fetch
    const t = new WsChatTransport({ baseUrl: 'http://x', token: 't', fetchImpl })
    await t.send('op', { text: 'see', clientId: 'c', attachments: [{ name: 'a.txt', mediaType: 'text/plain', dataUrl: 'data:text/plain;base64,aGVsbG8=' }] })
    const body = JSON.parse((vi.mocked(fetchImpl).mock.calls[0][1] as RequestInit).body as string)
    expect(body.attachments).toEqual([{ name: 'a.txt', mime: 'text/plain', data: 'aGVsbG8=' }])
  })
})
