import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SettingsModal, fmtBytes } from '../src/renderer/src/components/SettingsModal'
import { useStore } from '../src/renderer/src/store'

const machine = {
  host: 'ws-1', os: 'linux', cpus: 16, load1: 4,
  mem_total_bytes: 64 * 1024 ** 3, mem_available_bytes: 48 * 1024 ** 3,
  disk_total_bytes: 250 * 1024 ** 3, disk_free_bytes: 200 * 1024 ** 3,
}

afterEach(() => vi.unstubAllGlobals())

describe('Settings > Machine', () => {
  it('formats sizes at a glance', () => {
    expect(fmtBytes(512 * 1024 ** 2)).toBe('512 MB')
    expect(fmtBytes(7.8 * 1024 ** 3)).toBe('7.8 GB')
    expect(fmtBytes(200 * 1024 ** 3)).toBe('200 GB')
  })

  it('shows the remote machine and what is left on it', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => ({ ok: true, json: async () => (String(url).endsWith('/api/machine') ? { ok: true, machine, history: [{ at: 1000, mem: 20, disk: 80, cpu: 10 }, { at: 1002, mem: 25, disk: 80, cpu: 25 }] } : { ok: true, settings: {}, accounts: [] }) })))
    useStore.setState({ config: { apiUrl: 'http://127.0.0.1:8787', token: 't', localHost: 'laptop' } as any })
    render(<SettingsModal onClose={() => {}} section="machine" />)
    await waitFor(() => expect(screen.getByText(/Remote: ws-1/)).toBeTruthy())
    expect(screen.getByText('48 GB free of 64 GB')).toBeTruthy()
    expect(screen.getByText('200 GB free of 250 GB')).toBeTruthy()
    expect(screen.getByText('load 4.0 on 16 cores')).toBeTruthy()
    expect(screen.getByRole('img', { name: 'Memory over the last 30 minutes' })).toBeTruthy()
    expect(screen.getAllByText('25%').length).toBe(2) // memory and CPU now
    expect(screen.getByText('80%')).toBeTruthy()
  })
})
