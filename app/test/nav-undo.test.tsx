import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import fixture from './fixtures/snapshot.json'
import type { Snapshot } from '../src/shared/snapshot'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { useStore } from '../src/renderer/src/store'
const { runCmd } = vi.hoisted(() => ({ runCmd: vi.fn() }))
vi.mock('../src/renderer/src/api', () => ({ useApis: () => ({ cmdr: { runCmd } }) }))
let snap: Snapshot
beforeEach(() => {
  vi.useFakeTimers(); localStorage.clear()
  vi.stubGlobal('PointerEvent', MouseEvent); vi.stubGlobal('CSS', { escape: (s: string) => s })
  runCmd.mockReset().mockResolvedValue({ ok: true, stdout: '{}', stderr: '', exit: 0 })
  snap = structuredClone(fixture) as unknown as Snapshot
  snap.display!.workstreams = snap.display!.workstreams.slice(0, 2).map((w, i) => ({ ...w, key: `demo:${i}`, project: 'demo', bead_id: `item${i}`, title: `Row ${i}`, tracks: [], scope: undefined }))
  snap.display!.asks = []
  useStore.setState({ snapshot: snap, completed: {}, selected: { kind: 'commander' }, cursor: 'commander', conn: 'live' })
})
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })
async function drag(nest = false) {
  const rows = [...document.querySelectorAll<HTMLElement>('[data-nav-key]')]
  rows.forEach((r, i) => vi.spyOn(r, 'getBoundingClientRect').mockReturnValue({ top: i * 50, bottom: i * 50 + 40, height: 40 } as DOMRect))
  fireEvent.pointerDown(rows[1], { button: 0, clientX: 100, clientY: 70 })
  fireEvent.pointerMove(window, { clientX: nest ? 150 : 100, clientY: nest ? 45 : 0 })
  fireEvent.pointerUp(window)
  await act(async () => { await vi.advanceTimersByTimeAsync(60) })
}
async function settle() { await act(async () => { await Promise.resolve() }) }
it('offers Undo only after success and restores the old top-level rank', async () => {
  let resolve!: (v: unknown) => void
  runCmd.mockImplementationOnce(() => new Promise((r) => { resolve = r }))
  render(<Navigator />); await drag()
  expect(runCmd).toHaveBeenCalledTimes(1)
  expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull()
  await act(async () => resolve({ ok: true, stdout: '{}', stderr: '', exit: 0 }))
  fireEvent.click(screen.getByRole('button', { name: 'Undo' })); await settle()
  expect(runCmd.mock.calls[1][0]).toEqual(['internal-place', '--project', 'demo', '--bead', 'item1', '--parent', '', '--to', '2'])
  expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull()
})
it.each(['metaKey', 'ctrlKey'])('%s+Z uses the saved position after a board update', async (modifier) => {
  render(<Navigator />); await drag()
  act(() => useStore.setState({ snapshot: { ...snap, display: { ...snap.display!, workstreams: [...snap.display!.workstreams].reverse() } } }))
  fireEvent.keyDown(window, { key: 'z', [modifier]: true }); await settle()
  expect(runCmd.mock.calls[1][0].slice(-2)).toEqual(['--to', '2'])
})
it('confirms nesting before commands, supports cancel, then undoes', async () => {
  render(<Navigator />); await drag(true)
  expect(screen.getByRole('dialog', { name: 'Nest Row 1 under Row 0?' })).toBeInTheDocument()
  expect(screen.getByText('This changes who manages it.')).toBeInTheDocument()
  expect(runCmd).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' })); await drag(true)
  fireEvent.click(screen.getByRole('button', { name: 'Nest' })); await settle()
  expect(runCmd.mock.calls[0][0]).toContain('item0')
  fireEvent.click(screen.getByRole('button', { name: 'Undo' })); await settle()
  expect(runCmd.mock.calls[1][0].slice(-4)).toEqual(['--parent', '', '--to', '2'])
})
it('expires after eight seconds and leaves text editing undo alone', async () => {
  render(<Navigator />); await drag()
  const input = document.createElement('textarea'); document.body.append(input)
  fireEvent.keyDown(input, { key: 'z', ctrlKey: true }); input.remove()
  expect(runCmd).toHaveBeenCalledTimes(1)
  await act(async () => { await vi.advanceTimersByTimeAsync(8000) })
  expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull()
  fireEvent.keyDown(window, { key: 'z', metaKey: true }); expect(runCmd).toHaveBeenCalledTimes(1)
})
it('clears Undo when the bead disappears', async () => {
  render(<Navigator />); await drag()
  act(() => useStore.setState({ snapshot: { ...snap, display: { ...snap.display!, workstreams: [snap.display!.workstreams[0]] } } }))
  expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull()
})
it('shows undo failures via moveError and permits retry', async () => {
  render(<Navigator />); await drag(); runCmd.mockRejectedValueOnce(new Error('connection lost'))
  fireEvent.click(screen.getByRole('button', { name: 'Undo' })); await settle()
  expect(screen.getByText('Could not undo the move: connection lost')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument()
})
it('does not offer Undo for a failed drop', async () => {
  runCmd.mockResolvedValueOnce({ ok: false, stdout: '', stderr: '', error: 'refused' })
  render(<Navigator />); await drag()
  expect(screen.getByText('Could not move it: refused')).toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull()
})
