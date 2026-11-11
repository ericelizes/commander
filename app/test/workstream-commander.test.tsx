// @vitest-environment jsdom
import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { WorkstreamCommander } from '../src/renderer/src/components/WorkstreamCommander'
import { useStore } from '../src/renderer/src/store'

beforeEach(() => useStore.setState({ config: { apiUrl: 'http://localhost:8787', token: 'fixture' }, selected: { kind: 'commander' } }))
afterEach(() => { cleanup(); vi.unstubAllGlobals(); useStore.setState({ config: null, selected: { kind: 'commander' } }) })

it('creates an urgent thread against fresh ownership and opens its separate conversation', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ project_id: 7, bead_id: 'nested', commander_id: 'commander', revision: 12 }) })
    .mockResolvedValueOnce({ ok: true, json: async () => ({ project_id: 7, bead_id: 'nested', commander_id: 'commander-urgent', revision: 13 }) })
  vi.stubGlobal('fetch', fetcher)
  render(<WorkstreamCommander projectId={7} beadId="nested" workstreamKey="qa:root" owner="commander" />)
  fireEvent.click(screen.getByRole('button', { name: 'Commander for this workstream' }))
  fireEvent.click(screen.getByRole('button', { name: 'Open urgent thread' }))
  await waitFor(() => expect(useStore.getState().selected).toEqual({ kind: 'agent', key: 'commander-urgent', ws: 'qa:root' }))
  expect(fetcher.mock.calls[0][0]).toContain('project_id=7&bead_id=nested')
  expect(fetcher.mock.calls[1][0]).toBe('http://localhost:8787/api/commanders')
  expect(JSON.parse(fetcher.mock.calls[1][1].body)).toMatchObject({ project_id: 7, bead_id: 'nested', expected_owner: 'commander', revision: 12 })
})

it('opens the existing owner without changing the tree', async () => {
  const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ commander_id: 'commander-existing', revision: 3 }) })
  vi.stubGlobal('fetch', fetcher)
  render(<WorkstreamCommander projectId={7} beadId="nested" workstreamKey="qa:root" owner="commander-existing" />)
  fireEvent.click(screen.getByRole('button', { name: 'Commander for this workstream' }))
  fireEvent.click(screen.getByRole('button', { name: 'Open responsible Commander' }))
  await waitFor(() => expect(useStore.getState().selected).toEqual({ kind: 'agent', key: 'commander-existing', ws: 'qa:root' }))
  expect(fetcher).toHaveBeenCalledTimes(1)
})

it('keeps the conversation in place when a concurrent handoff conflicts', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ commander_id: 'commander-urgent', revision: 5 }) })
    .mockResolvedValueOnce({ ok: false, status: 409, json: async () => ({ error: 'Ownership changed; reread the workstream' }) })
  vi.stubGlobal('fetch', fetcher)
  render(<WorkstreamCommander projectId={7} beadId="nested" workstreamKey="qa:root" owner="commander-urgent" />)
  fireEvent.click(screen.getByRole('button', { name: 'Commander for this workstream' }))
  fireEvent.click(screen.getByRole('button', { name: 'Return to parent Commander' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Ownership changed')
  expect(useStore.getState().selected).toEqual({ kind: 'commander' })
  expect(JSON.parse(fetcher.mock.calls[1][1].body)).toMatchObject({ inherit: true, expected_owner: 'commander-urgent', revision: 5 })
})
