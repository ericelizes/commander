import { cleanup, render, screen, fireEvent, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { Navigator } from '../src/renderer/src/components/Navigator'
import { PitchView } from '../src/renderer/src/components/PitchView'
import { useStore } from '../src/renderer/src/store'
import { allTracks, findTrack, linkLabel, openLeaves, openNested } from '../src/renderer/src/viewmodel'
import type { BoardRow, BoardTrack, SnapStep, SnapWorkstream, Snapshot } from '../src/shared/snapshot'

const track = (id: string, title: string, letter: string, children?: BoardTrack[]): BoardTrack => ({
  key: `p:${id}`, bead_id: id, letter, title, kind: 'working', text: 'Working', since: 0, progress: { done: 0, total: 1 }, working: 0, children,
})
const step = (id: string, nested: boolean, children?: SnapStep[]): SnapStep => ({ bead_id: id, title: id, status: 'open', state: 'in-progress', track: nested || undefined, children }) as SnapStep

const row: BoardRow = {
  key: 'p:w', n: 3, title: 'Root', project: 'p', bead_id: 'w', manager_role: 'manager-w', starred: false, kind: 'working', text: 'Working', since: 0,
  progress: { done: 0, total: 3 }, working: 0, flag: 0, unread: 0, gate: null,
  tracks: [track('w.1', 'One', '.1', [track('w.1.1', 'Deep', '.1.1')]), track('w.2', 'Two', '.2')],
}
const ws = { n: 3, project: 'p', project_id: 1, bead_id: 'w', title: 'Root', steps: [step('w.1', true, [step('w.1.1', true, [step('a', false)]), step('w.1.2', true)]), step('w.2', true, [step('b', false)])] } as unknown as SnapWorkstream
const snapshot = { workstreams: [ws], needs_you: [], display: { workstreams: [row], asks: [], commander: { kind: 'idle', text: '', since: 0, unread: 0 } } } as unknown as Snapshot

describe('a decision in a row standing in for its child', () => {
  it('draws its workflow, though the row reads the child title', () => {
    const shown = { ...row, title: 'Only child', scope: 'w.1', tracks: [], diagram: { id: 'w', title: 'Root', current: '', steps: [], tracks: [{ id: 'w.1', title: 'Only child', current: '', steps: [{ id: 's', title: 'A step of the child', kind: 'working', text: 'Working', lane: 'agents', actor: 'operators' }] }] } }
    useStore.setState({ snapshot: { ...snapshot, display: { ...snapshot.display!, workstreams: [shown] } } as unknown as Snapshot })
    render(<PitchView item={{ project: 'p', bead_id: 'a', title: 'Pick', workstream: 'Root', track_id: 'w.1', rank: 3, decision: { question: 'Pick?' } } as never} />)
    expect(screen.getByText(/A step of the child/)).toBeInTheDocument()
    cleanup()
  })
})

describe('the tree in the view model', () => {
  it('walks every depth', () => {
    expect(allTracks(row).map((t) => t.letter)).toEqual(['.1', '.1.1', '.2'])
    expect(findTrack(row, 'w.1.1')?.title).toBe('Deep')
    expect(openNested(ws).map((s) => s.bead_id)).toEqual(['w.1', 'w.1.1', 'w.1.2', 'w.2'])
    expect(openLeaves(ws).map((s) => s.bead_id)).toEqual(['w.1.1', 'w.1.2', 'w.2'])
  })

  it('names a deep bead by its full number', () => {
    expect(linkLabel(snapshot, 'a', 'workflow')).toBe('Workstream 3.1.1, Workflow')
    expect(linkLabel(snapshot, 'w')).toBe('Workstream 3, Overview')
  })
})

describe('the sidebar', () => {
  afterEach(cleanup)
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('nav.tab', 'all')
    useStore.setState({ snapshot, completed: {}, selected: { kind: 'commander' }, workflowOpen: null })
  })

  it('shows two levels and opens the next with a chevron', () => {
    render(<Navigator />)
    const under = screen.getByRole('group', { name: 'Workstreams under Root' })
    expect(within(under).getByText('3.1')).toBeInTheDocument()
    expect(within(under).getByText('3.2')).toBeInTheDocument()
    expect(screen.queryByText('Deep')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show 1 workstreams under One' }))
    expect(screen.getByText('3.1.1')).toBeInTheDocument()
    expect(screen.getByText('Deep')).toBeInTheDocument()
  })

  it('lights the nested row that is open, not its parent', () => {
    useStore.setState({ selected: { kind: 'ws', key: 'p:w' }, workflowOpen: { ws: 'p:w', track: 'w.2', at: 1 } })
    render(<Navigator />)
    expect(screen.getByText('Two').closest('.row')).toHaveClass('selected')
    expect(screen.getByText('Root').closest('.row')).not.toHaveClass('selected')
  })

  it('lights the parent while the open row is folded away', () => {
    useStore.setState({ selected: { kind: 'ws', key: 'p:w' }, workflowOpen: { ws: 'p:w', track: 'w.1.1', at: 1 } })
    render(<Navigator />)
    expect(screen.getByText('Root').closest('.row')).toHaveClass('selected')
  })

  it('a row standing in for its one child opens scoped to it', () => {
    useStore.setState({ snapshot: { ...snapshot, display: { ...snapshot.display!, workstreams: [{ ...row, title: 'Only child', scope: 'w.1', tracks: [] }] } } })
    render(<Navigator />)
    fireEvent.click(screen.getByText('Only child'))
    expect(useStore.getState().workflowOpen).toMatchObject({ ws: 'p:w', track: 'w.1' })
  })
})
