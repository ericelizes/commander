// Mail waiting for an agent is a small "N waiting" pill; hovering lists the
// messages, clicking the pill sends them now.
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { MailChip } from '../src/renderer/src/components/MailChip'
import { useStore } from '../src/renderer/src/store'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

beforeEach(() => {
  useStore.setState({
    snapshot: { ...snap, unread: { commander: 2 } },
    config: { apiUrl: 'http://x', token: 't' } as never,
  })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('MailChip', () => {
  it('shows "2 waiting" and lists who, what and when on hover', async () => {
    const mail = [
      { id: '1', from: 'manager-x', subject: 'PR ready', preview: 'details', sent_at: new Date(Date.now() - 5 * 60_000).toISOString() },
      { id: '2', from: 'commander', subject: 'CI green', preview: '', sent_at: new Date().toISOString() },
    ]
    const fetchMock = vi.fn(async (_url: string) => new Response(JSON.stringify({ ok: true, mail })))
    vi.stubGlobal('fetch', fetchMock)
    render(<MailChip role="commander" />)
    const pill = screen.getByRole('button', { name: /2 waiting/ })
    expect(pill.textContent).toBe('2 waiting')
    fireEvent.mouseEnter(pill.parentElement!)
    expect(await screen.findByText('manager-x')).toBeTruthy()
    expect(screen.getByText('PR ready')).toBeTruthy()
    expect(screen.getByText('5m ago')).toBeTruthy()
    expect(screen.getByText('just now')).toBeTruthy()
    expect(fetchMock.mock.calls[0][0]).toBe('http://x/api/chat/commander/mail')
  })

  it('click sends them now and says so', async () => {
    const fetchMock = vi.fn(async () => new Response('{}'))
    vi.stubGlobal('fetch', fetchMock)
    render(<MailChip role="commander" />)
    fireEvent.click(screen.getByRole('button', { name: /2 waiting/ }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('http://x/api/chat/commander/mail-now', expect.anything()))
    expect(screen.getByText('Sending 2 to the agent…')).toBeTruthy()
  })
})
