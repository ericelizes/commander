import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { NeedsYouItem } from '../../../features/decisions'
import type { Snapshot } from '../../../shared/snapshot'
import { useStore } from '../store'
import { PitchChoices } from './PitchView'

const item: NeedsYouItem = {
  project: 'sample', bead_id: 'sample-1.2', title: 'Approve the plan',
  workstream: 'Sample workflow', rank: 1, plan_revision: 3,
  decision: { question: 'Proceed?', options: [{ label: 'Approve', approves: true }, { label: 'Not yet' }] },
}
let pending: NeedsYouItem[]
let commandResult: { ok: boolean; stderr: string; exit: number }
let chatFails: boolean
let calls: { url: string; body?: { argv?: string[]; text?: string } }[]
let finishCommand: (() => void) | undefined
let holdCommand: boolean

beforeEach(() => {
  pending = [item]
  commandResult = { ok: true, stderr: '', exit: 0 }
  chatFails = false
  holdCommand = false
  finishCommand = undefined
  calls = []
  useStore.setState({ config: { apiUrl: 'http://fixture', token: 'fixture' }, snapshot: { needs_you: [item], workstreams: [] } as unknown as Snapshot })
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    if (url.endsWith('/api/snapshot')) return Response.json({ needs_you: pending })
    if (url.endsWith('/api/cmd')) {
      if (holdCommand) await new Promise<void>((resolve) => { finishCommand = resolve })
      return Response.json(commandResult)
    }
    if (url.endsWith('/api/chat/commander/send')) {
      if (chatFails) throw new Error('offline')
      return Response.json({ ok: true })
    }
    throw new Error(`Unexpected request: ${url}`)
  }))
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

function mount() {
  render(<PitchChoices item={item} question="Proceed?" where="#1 Sample workflow" version={2} />)
  fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
}
const commands = () => calls.filter((c) => c.url.endsWith('/api/cmd'))
const messages = () => calls.filter((c) => c.url.endsWith('/api/chat/commander/send'))

it('records the exact choice/revision/context before the FYI and clears only this card', async () => {
  const otherProject = { ...item, project: 'other' }
  useStore.setState({ snapshot: { needs_you: [item, otherProject], workstreams: [] } as unknown as Snapshot })
  mount()
  expect(await screen.findByText('Answer recorded: Approve')).toBeInTheDocument()
  await waitFor(() => expect(messages()).toHaveLength(1))
  expect(commands()[0].body?.argv).toEqual([
    'internal-resolve-needs', '--project', 'sample', '--bead', 'sample-1.2', '--action', 'decide',
    '--revision', '3', '--choice', 'Approve', '--text', 'Pitch: Approve the plan (v2)\nWhere: #1 Sample workflow\nId: sample-1.2',
  ])
  expect(calls.indexOf(commands()[0])).toBeLessThan(calls.indexOf(messages()[0]))
  expect(messages()[0].body?.text).toContain('FYI: Answer already recorded: Approve\nProject: sample')
  expect(useStore.getState().snapshot!.needs_you).toEqual([otherProject])
  expect(screen.getByRole('button', { name: /✓\s*Approve/ })).toBeDisabled()
})

it('keeps the card and enables retry on recording failure, without sending an FYI', async () => {
  commandResult = { ok: false, stderr: '{"error":"gate changed"}', exit: 1 }
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not record answer: gate changed')
  expect(useStore.getState().snapshot!.needs_you).toEqual([item])
  expect(messages()).toHaveLength(0)
  expect(screen.getByRole('button', { name: /Approve/ })).toBeEnabled()
  commandResult = { ok: true, stderr: '', exit: 0 }
  fireEvent.click(screen.getByRole('button', { name: /Approve/ }))
  expect(await screen.findByText('Answer recorded: Approve')).toBeInTheDocument()
})

it('keeps the recorded/check state if FYI delivery fails', async () => {
  chatFails = true
  mount()
  expect(await screen.findByRole('alert')).toHaveTextContent('Answer recorded, but could not notify Commander: offline')
  expect(screen.getByText('Answer recorded: Approve')).toBeInTheDocument()
  expect(screen.getByRole('button', { name: /✓\s*Approve/ })).toBeDisabled()
  expect(useStore.getState().snapshot!.needs_you).toEqual([])
  expect(commands()).toHaveLength(1)
})

it('does not claim this choice or send its FYI when the answer was already recorded elsewhere', async () => {
  pending = []
  mount()
  expect(await screen.findByText('Already answered elsewhere.')).toBeInTheDocument()
  expect(commands()[0].body?.argv).toContain('--resume-only')
  expect(messages()).toHaveLength(0)
  expect(screen.queryByText('✓')).toBeNull()
  expect(screen.getByRole('button', { name: /Approve/ })).toBeDisabled()
})

it('disables repeated choices until recording completes and preserves a newer revision', async () => {
  holdCommand = true
  mount()
  await waitFor(() => expect(finishCommand).toBeDefined())
  fireEvent.click(screen.getByRole('button', { name: /Not yet/ }))
  expect(commands()).toHaveLength(1)
  expect(messages()).toHaveLength(0)
  expect(screen.getByText('Recording…')).toBeInTheDocument()
  const newer = { ...item, plan_revision: 4 }
  useStore.setState({ snapshot: { needs_you: [newer], workstreams: [] } as unknown as Snapshot })
  finishCommand!()
  expect(await screen.findByText('Answer recorded: Approve')).toBeInTheDocument()
  expect(useStore.getState().snapshot!.needs_you).toEqual([newer])
})
