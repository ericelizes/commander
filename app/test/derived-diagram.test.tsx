// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { BoardRow, Diagram } from '../src/shared/snapshot'
import { DerivedDiagram, diagramFor, flowOf } from '../src/renderer/src/components/DerivedDiagram'
import { PitchView, withoutDrawnGraphs } from '../src/renderer/src/components/PitchView'
import { useStore } from '../src/renderer/src/store'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const track: Diagram = {
  id: 'lab-bex.2',
  title: 'Nightly export to the warehouse',
  current: 'lab-bex.2.3',
  steps: [
    { id: 'lab-bex.2.1', title: 'Export job built', kind: 'done', text: 'Done', lane: 'agents', actor: 'operators' },
    { id: 'lab-bex.2.2', title: 'CI green on the export PR', kind: 'waiting', text: 'Waiting for CI', lane: 'elsewhere', actor: 'automatic', who: 'CI, runs on each push to the PR', after: ['lab-bex.2.1'] },
    { id: 'lab-bex.2.3', title: 'Export PR approved by reviewers', kind: 'idle', text: 'Not reached', lane: 'elsewhere', actor: 'reviewers', who: 'reviewer-a, reviewer-b', gate: true, approvers: 'reviewer-a, reviewer-b', criteria: 'Both reviewers approve the PR on GitHub.', after: ['lab-bex.2.2'] },
    { id: 'lab-bex.2.4', title: 'Greenlight merging the export to main', kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you', after: ['lab-bex.2.3'] },
    { id: 'lab-bex.2.5', title: 'Export live in production', kind: 'idle', text: 'Not started', lane: 'agents', actor: 'operators', after: ['lab-bex.2.4'] },
  ],
  findings: [{ check: 'gate-criteria', bead: 'lab-bex.2.4', title: 'Greenlight merging the export to main', why: 'the gate does not say what counts as passed', fix: 'br update' }],
}

const ws: Diagram = {
  id: 'lab-bex',
  title: 'Billing exports',
  current: 'lab-bex.3',
  steps: [
    { id: 'lab-bex.1', title: 'Workflow approved', kind: 'done', text: 'Done', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
    { id: 'lab-bex.2', title: 'Nightly export to the warehouse', kind: 'waiting', text: 'Waiting for CI', lane: 'elsewhere', actor: 'automatic', who: 'CI, runs on each push to the PR', track: true },
    { id: 'lab-bex.3', title: 'Finance sign-off', kind: 'needs', text: 'Needs you', lane: 'you', actor: 'you', track: true },
    { id: 'lab-bex.9', title: 'Export docs updated', kind: 'waiting', text: 'Waiting for Manager to close', lane: 'agents', actor: 'manager' },
    { id: 'lab-bex.4', title: 'Backfill last quarter', kind: 'stalled', text: 'State missing', lane: '', actor: '' },
  ],
  tracks: [track],
}

const row = (d?: Diagram) => ({ key: 'lab:lab-bex', title: 'Billing exports', project: 'lab', diagram: d, tracks: [] }) as unknown as BoardRow

describe('DerivedDiagram', () => {
  it('draws the execution workflow: a line per actor, steps in order on their actor, an arrow at each handoff', () => {
    const { container } = render(<DerivedDiagram diagram={track} />)
    expect([...container.querySelectorAll('.xf-actor')].map((e) => e.textContent)).toEqual(['You', 'Agents', 'Reviewers', 'Automatic'])
    const col = (title: string) => (screen.getByText(title).closest('.xf-step') as HTMLElement).style.gridColumn
    expect(col('Export job built')).toBe('2')
    expect(col('CI green on the export PR')).toBe('4')
    expect(col('Export PR approved by reviewers')).toBe('3')
    expect(col('Greenlight merging the export to main')).toBe('1')
    // operators -> automatic -> reviewers -> you -> operators: four handoffs
    expect(container.querySelectorAll('.xf-handoff')).toHaveLength(4)
  })

  it('marks where the work is, who approves a checkpoint and what passes it, in the board colours', () => {
    const { container } = render(<DerivedDiagram diagram={track} />)
    // cmdrd's current step is not reached yet, so Now is the live step before it
    expect(container.querySelector('.is-now')).toHaveTextContent('CI green on the export PR')
    const review = screen.getByText('Export PR approved by reviewers').closest('.xf-step')!
    expect(review).toHaveTextContent('reviewer-a, reviewer-b approve')
    // what passes a checkpoint is folded under Read more
    expect(review.querySelector('.xf-more')).toHaveTextContent('Passes when: Both reviewers approve the PR on GitHub.')
    expect(screen.getByText('Waiting for CI')).toHaveClass('xf-top', 'agent-waiting')
    expect(screen.getByText('CI, runs on each push to the PR')).toBeInTheDocument()
    const merge = screen.getByText('Greenlight merging the export to main').closest('.xf-step')!
    expect(merge).toHaveTextContent(/^Not reached/)
    expect(merge).not.toHaveTextContent('you approve')
    expect(merge).toHaveTextContent('No criteria yet')
    expect(screen.getByText('Export live in production').closest('.xf-step')).toHaveTextContent(/^Final: Not started/)
  })

  it('leaves the plan checks to the Manager: no count of failing checks', () => {
    render(<DerivedDiagram diagram={track} />)
    expect(screen.queryByText(/check fails|checks fail/)).toBeNull()
  })

  it('opens a track from its card, and draws a step nobody holds on the Manager line, dashed', async () => {
    const open = vi.fn()
    const { container } = render(<DerivedDiagram diagram={ws} onOpenTrack={open} />)
    await userEvent.click(screen.getByRole('button', { name: /Nightly export/ }))
    expect(open).toHaveBeenCalledWith('lab-bex.2')
    const lost = screen.getByText('Backfill last quarter').closest('.xf-step') as HTMLElement
    expect(lost.style.gridColumn).toBe(String([...container.querySelectorAll('.xf-actor')].findIndex((e) => e.textContent === 'Commander') + 1))
    expect(lost).toHaveClass('is-noactor')
    expect(container.querySelector('.is-now')).toHaveTextContent('Finance sign-off')
  })

  it('draws every done step: nothing folds, the pane scrolls', () => {
    const many: Diagram = { ...track, steps: [...Array(6)].map((_, i) => ({ ...track.steps[0], id: `d${i}`, title: `Done ${i}` })).concat(track.steps.slice(1)) }
    render(<DerivedDiagram diagram={many} />)
    expect(screen.getByText('Done 0').closest('.xf-step')).toHaveClass('is-done')
    expect(screen.queryByText(/earlier step/)).toBeNull()
  })

  it('draws a gate waiting on a reviewer as two moves: the review on Reviewers, then your greenlight on You', () => {
    const d: Diagram = {
      id: 'lab-qm.1',
      title: 'Key renews',
      current: 'lab-qm.1.7#wait',
      steps: [
        { id: 'lab-qm.1.6', title: 'PR mergeable', kind: 'done', text: 'Done', lane: 'agents', actor: 'manager' },
        { id: 'lab-qm.1.7#wait', title: 'reviewer-a reviews PR #4116', kind: 'waiting', text: 'Waiting for review', lane: 'elsewhere', actor: 'reviewers', who: 'waiting on reviewer-a (#4116)', after: ['lab-qm.1.6'] },
        { id: 'lab-qm.1.7', title: 'Greenlight merging the key renewal PR', kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you', criteria: 'The user says merge.', after: ['lab-qm.1.7#wait'] },
      ],
    }
    const { container } = render(<DerivedDiagram diagram={d} />)
    const head = [...container.querySelectorAll('.xf-actor')].map((e) => e.textContent)
    const line = (title: string) => head[Number((screen.getByText(title).closest('.xf-step') as HTMLElement).style.gridColumn) - 1]
    expect(line('reviewer-a reviews PR #4116')).toBe('Reviewers')
    expect(line('Greenlight merging the key renewal PR')).toBe('You')
    expect(screen.getByText('reviewer-a reviews PR #4116').closest('.xf-step')).toHaveClass('is-now')
    // the current step is the strong card; the checkpoint after it is muted, not yellow
    const later = screen.getByText('Greenlight merging the key renewal PR').closest('.xf-step')!
    expect(later).toHaveTextContent(/^Final: Not reached/)
    expect(later).toHaveClass('is-future')
    expect(later).not.toHaveClass('is-next')
    expect(container.querySelectorAll('.xf-handoff')).toHaveLength(2)
  })

  it('with no current step the first open step is Now, later checkpoints future, and a done one never future', () => {
    const cp = { kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you', criteria: 'ok' } as const
    const d: Diagram = {
      id: 'x.1',
      title: 'No current',
      current: '',
      steps: [
        { ...cp, id: 'x.1.1', title: 'Gate passed', kind: 'done', text: 'Done' },
        { ...cp, id: 'x.1.2', title: 'Gate first' },
        { ...cp, id: 'x.1.3', title: 'Gate second' },
      ],
    }
    const { container } = render(<DerivedDiagram diagram={d} />)
    const card = (t: string) => screen.getByText(t).closest('.xf-step')!
    expect(container.querySelectorAll('.is-now')).toHaveLength(1)
    expect(card('Gate first')).toHaveClass('is-now')
    expect(card('Gate first')).not.toHaveClass('is-future')
    expect(card('Gate second')).toHaveClass('is-future')
    expect(container.querySelector('.is-next')).toBeNull()
    expect(card('Gate passed')).toHaveClass('is-done')
    expect(card('Gate passed')).not.toHaveClass('is-future')
  })
})

describe('diagramFor', () => {
  it('is the track asked for, the workstream, or the one open track a workstream is', () => {
    expect(diagramFor(row(ws), 'lab-bex.2')).toBe(track)
    expect(diagramFor(row(ws))).toBe(ws)
    const only: Diagram = { ...ws, steps: [ws.steps[0], ws.steps[1]] }
    expect(diagramFor(row(only))).toBe(track)
    expect(diagramFor(row(undefined))).toBeNull()
  })
})

describe('a pitch draws the derived diagram, never its own', () => {
  const drawn = '```cmdr-graph\n{"nodes": [{"id": "g", "title": "Hand drawn checkpoint", "gate": true}]}\n```'
  const context = `**The plan**\n\n- Ship the export.\n\n**Execution workflow**\n\n${drawn}`
  const item = { project: 'lab', bead_id: 'lab-bex.2.4', title: 'Greenlight merging the export to main', workstream: 'Billing exports', track: 'Nightly export to the warehouse', track_id: 'lab-bex.2', rank: 1, decision: { question: 'Merge the export?', context, options: [{ label: 'Approve' }] } }
  const setup = (d?: Diagram) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ pitches: [] }), { status: 200, headers: { 'content-type': 'application/json' } })))
    useStore.setState({
      config: { apiUrl: 'http://127.0.0.1:9', token: 't' },
      snapshot: { needs_you: [], workstreams: [], display: { workstreams: [row(d)] } } as never,
    })
  }

  it('ignores the card’s cmdr-graph and shows the track’s diagram', async () => {
    setup(ws)
    const { container } = render(<PitchView item={item as never} />)
    await waitFor(() => expect(container.querySelector('.dg')).not.toBeNull())
    expect(screen.queryByText('Hand drawn checkpoint')).toBeNull()
    expect(screen.queryByText(/cmdr-graph/)).toBeNull()
    expect(screen.getByText('Export PR approved by reviewers')).toBeInTheDocument()
    expect(container.querySelectorAll('.dg')).toHaveLength(1)
  })

  it('is the diagram alone: no pitch text under it', () => {
    setup(undefined)
    render(<PitchView item={item as never} />)
    expect(screen.queryByText('Hand drawn checkpoint')).toBeNull()
    expect(screen.queryByText(/Read the pitch/)).toBeNull()
  })

  it('strips drawn graphs, complete or cut off', () => {
    expect(withoutDrawnGraphs(`a\n\n${drawn}\n\nb`)).toBe('a\n\nb')
    expect(withoutDrawnGraphs('a\n\n```cmdr-graph\n{"nodes": [')).toBe('a')
  })
})

describe('every header draws the sequence', () => {
  it('builds the diagram from the workflow when cmdrd sends none, on at least two lines', () => {
    const r = {
      key: 'lab:lab-x', bead_id: 'lab-x', title: 'X', tracks: [],
      workflow: { current: 'lab-x.2', steps: [
        { id: 'lab-x.1', title: 'Workflow approved', kind: 'done', text: 'Done', gate: true, who: 'you' },
        { id: 'lab-x.2', title: 'Build it', kind: 'working', text: 'Operator working' },
      ] },
    } as unknown as BoardRow
    const d = diagramFor(r)!
    expect(d.steps.map((s) => s.actor)).toEqual(['you', 'operators'])
    const { container } = render(<DerivedDiagram diagram={d} />)
    expect(container.querySelectorAll('.xf-actor').length).toBeGreaterThanOrEqual(2)
    expect(container.querySelectorAll('.xf-handoff')).toHaveLength(1)
  })

  it('draws a second line even when every step is on one, and says when nothing is planned', () => {
    const one: Diagram = { id: 'a', title: 'A', steps: [{ id: 'a.1', title: 'Decide', kind: 'needs', text: 'Needs you', lane: 'you', actor: 'you' }] }
    const { container } = render(<DerivedDiagram diagram={one} />)
    expect([...container.querySelectorAll('.xf-actor')].map((e) => e.textContent)).toEqual(['You', 'Commander'])
    cleanup()
    render(<DerivedDiagram diagram={{ id: 'b', title: 'B', steps: [] }} />)
    expect(screen.getByText('Nothing planned yet')).toBeInTheDocument()
  })
})

describe('steps nested under a step', () => {
  const nested: Diagram = {
    id: 'lab-hub.1',
    title: 'Importer rollout',
    steps: [
      {
        id: 'lab-hub.1.2',
        title: 'Importer ships',
        kind: 'waiting',
        text: 'Waiting for CI',
        lane: 'elsewhere',
        actor: 'automatic',
        progress: '4 of 9 done',
        children: [
          { id: 'lab-hub.1.2.5', title: 'Importer PR open', kind: 'working', text: 'Operator working', lane: 'agents', actor: 'operators' },
          { id: 'lab-hub.1.2.8', title: 'Greenlight running the backfill', kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you', criteria: 'The user says run it.' },
        ],
      },
    ],
  }

  it('draws an open stage as its steps, each marked with the stage, not as a box of its own', () => {
    render(<DerivedDiagram diagram={nested} />)
    expect(screen.queryByText('Importer ships')).toBeNull()
    expect(screen.getByText('Importer PR open')).toBeTruthy()
    expect(screen.getByText('Greenlight running the backfill')).toBeTruthy()
    expect(screen.getByText('Passes when: The user says run it.')).toBeTruthy()
    expect(screen.queryByText(/^Under/)).toBeNull()
    expect(screen.getAllByText('Part of: Importer ships')).toHaveLength(1)
  })
})

describe('every ask is red', () => {
  const d: Diagram = {
    id: 'lab-t',
    title: 'Job workers',
    current: 'lab-t.2',
    steps: [
      {
        id: 'lab-t.1', title: 'Prod deployed with every setting off', kind: 'needs', text: 'Needs you · greenlight pushing', lane: 'you', actor: 'you', progress: '3 of 5 done',
        children: [{ id: 'lab-t.1.1', title: 'Facts checked', kind: 'stalled', text: 'Agent stopped', lane: 'agents', actor: 'operators' }],
      },
      { id: 'lab-t.2', title: 'Greenlight pushing', kind: 'needs', text: 'Needs you', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
      { id: 'lab-t.3', title: 'Another ask', kind: 'needs', text: 'Needs you', lane: 'you', actor: 'you' },
    ],
  }
  it('an open stage is drawn as its steps; every ask is Now, and red', () => {
    const f = flowOf(d)
    const by = (id: string) => f.find((x) => x.id === id)
    expect(by('lab-t.1')).toBeUndefined()
    expect(by('lab-t.1.1')!.inside).toBe('Prod deployed with every setting off')
    expect(f.filter((x) => x.now).map((x) => x.id)).toEqual(['lab-t.2', 'lab-t.3'])
    expect(f.filter((x) => x.kind === 'needs').map((x) => x.id)).toEqual(['lab-t.2', 'lab-t.3'])
  })
})

it('with no current step, Now is the first step not done, not a later checkpoint', () => {
  const d: Diagram = {
    id: 'lab-p',
    title: 'Publish',
    steps: [
      { id: 'lab-p.1', title: 'History rewritten', kind: 'done', text: 'Done', lane: 'agents', actor: 'operators' },
      { id: 'lab-p.2', title: 'Old commits removed', kind: 'waiting', text: 'Waiting until Fri', lane: 'elsewhere', actor: 'automatic' },
      { id: 'lab-p.3', title: 'Greenlight making it public', kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
    ],
  }
  expect(flowOf(d).filter((x) => x.now).map((x) => x.id)).toEqual(['lab-p.2'])
})

it('your checkpoint, when it is now, reads "Needs your approval" alone, in red', () => {
  const d: Diagram = {
    id: 'lab-d',
    title: 'Deploy',
    current: 'lab-d.2',
    steps: [
      { id: 'lab-d.1', title: 'Packet prepared', kind: 'done', text: 'Done', lane: 'agents', actor: 'operators' },
      { id: 'lab-d.2', title: 'Dev VMs confirmed done', kind: 'needs', text: 'Needs you', lane: 'you', actor: 'you', gate: true, approvers: 'you', criteria: 'The user confirms.' },
    ],
  }
  render(<DerivedDiagram diagram={d} />)
  const card = screen.getByText('Dev VMs confirmed done').closest('.xf-step')!
  expect(card).toHaveClass('is-now', 'kind-needs')
  expect(card).toHaveTextContent(/^Final: Needs your approval/)
  expect(card).not.toHaveTextContent('you approve')
  expect(card).not.toHaveTextContent('Now · checkpoint')
  expect(card.querySelectorAll('.xf-kicker')).toHaveLength(1)
})

it('draws every step not reached yet dashed, a work step as much as a checkpoint', () => {
  render(<DerivedDiagram diagram={track} />)
  expect(screen.getByText('Greenlight merging the export to main').closest('.xf-step')).toHaveClass('is-future')
  expect(screen.getByText('Export live in production').closest('.xf-step')).toHaveClass('is-future')
  expect(screen.getByText('CI green on the export PR').closest('.xf-step')).not.toHaveClass('is-future')
})

it('reads in time order: done, then Now, then the rest happening now, then ahead, parked last', () => {
  const d: Diagram = {
    id: 'lab-e',
    title: 'Rules report',
    current: 'lab-e.6',
    steps: [
      { id: 'lab-e.1', title: 'Spec approved', kind: 'done', text: 'Done', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
      { id: 'lab-e.2', title: 'Messages approved', kind: 'idle', text: 'Parked until Fri 10:01 AM', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
      { id: 'lab-e.3', title: 'CI PR approved by reviewer-a', kind: 'waiting', text: 'Waiting for review', lane: 'elsewhere', actor: 'reviewers' },
      { id: 'lab-e.4', title: 'Report PR approved by reviewer-b', kind: 'waiting', text: 'Waiting for review', lane: 'elsewhere', actor: 'reviewers' },
      { id: 'lab-e.5', title: 'Greenlight merging the report', kind: 'idle', text: 'Not reached', lane: 'you', actor: 'you', gate: true, approvers: 'you' },
      { id: 'lab-e.6', title: 'Report deployed', kind: 'idle', text: 'Not started', lane: 'agents', actor: 'manager' },
    ],
  }
  const f = flowOf(d)
  expect(f.map((x) => x.id)).toEqual(['lab-e.1', 'lab-e.3', 'lab-e.4', 'lab-e.5', 'lab-e.6', 'lab-e.2'])
  // cmdrd's current is not started: Now is the first live step, and nothing before it is open
  expect(f.find((x) => x.now)!.id).toBe('lab-e.3')
  const at = f.findIndex((x) => x.now)
  expect(f.slice(0, at).every((x) => x.done)).toBe(true)
})

describe('untitled steps', () => {
  it('never shows a bead id for a step with no title', () => {
    const d: Diagram = {
      id: 'lab-bex',
      title: 'Billing exports',
      steps: [{ id: 'lab-bex.7.2', title: '', kind: 'idle', text: 'Not started', lane: 'agents', actor: 'operators' }],
    }

    const { container } = render(<DerivedDiagram diagram={d} />)
    expect(container.textContent).toContain('Untitled step')
    expect(container.textContent).not.toMatch(/lab-bex(\.\d+)*/)
  })
})

it('keeps failure cause, owner and recovery target on the Manager lifeline', () => {
 const why = 'Automation failure: deadline missed. Owner: manager-sample-flow. Next: review existing report on sample-flow.1.'
 const d: Diagram = { id: 'sample-flow', title: 'Sample flow', steps: [{ id: 'sample-flow.1', title: 'Review existing result', kind: 'stalled', text: 'Manager review overdue', why, actor: 'manager', lane: 'agents' }] }
 const {container} = render(<DerivedDiagram diagram={d} />)
 expect(container.querySelector('.kind-stalled')).toBeInTheDocument()
 expect(container.querySelector('.agent-stalled')).toBeInTheDocument()
 expect(screen.getByText(why)).toBeInTheDocument()
 expect(container.querySelector('.kind-needs')).toBeNull()
})
