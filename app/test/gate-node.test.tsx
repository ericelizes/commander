// A gate is a labelled band in the graph: who it is for, where it stands, and
// its whole title, wrapped; a PR or ticket ref in the title is a pill.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { gateBandH, type Canvas } from '../src/renderer/src/graph'
import { CanvasWorld } from '../src/renderer/src/components/TaskGraph'

import { useIntegrations } from '../src/features/linear/settings'

beforeEach(() => useIntegrations.setState({ linearWorkspace: 'acme' }))
afterEach(() => { cleanup(); useIntegrations.setState({ linearWorkspace: '' }) })

const gate = (id: string, title: string, state: string, approvers: string[], gateKind: string, tone: string, y: number) =>
  ({ id, title, state, gate: true, approvers, gateKind, tone, x: 0, y, w: 460, h: gateBandH(title) }) as Canvas['cards'][number]

describe('gate band', () => {
  it('draws each gate as a band with its kind, state and the full title', () => {
    const long = 'Run the test-host report once the new schedule is live and the dry run is clean on every host (TEAM-101)'
    const canvas: Canvas = {
      cards: [
        gate('a', 'Workflow approved', 'needs-user', ['user'], 'yours', 'yellow', 0),
        gate('b', long, 'not-started', ['user'], 'yours', 'idle', 100),
        gate('c', 'PR approval', 'done', ['reviewer-a'], 'reviewer', 'green', 200),
      ],
      boxes: [],
      edges: [],
      width: 500,
      height: 300,
    }
    const { container } = render(<CanvasWorld canvas={canvas} />)
    expect(container.querySelectorAll('.gate-band')).toHaveLength(3)
    expect(container.querySelectorAll('.gate-band.tone-yellow, .gate-band.tone-idle, .gate-band.tone-green')).toHaveLength(3)
    expect(screen.getAllByText('Your greenlight')).toHaveLength(2)
    expect(screen.getByText('Reviewer')).toBeTruthy()
    expect(screen.getByText('Needs you')).toBeTruthy()
    expect(screen.getByText(/Run the test-host report .* every host/)).toBeTruthy()
    expect(screen.getByRole('link', { name: /TEAM-101/ })).toBeTruthy()
  })

  it('grows the band with a long title so none of it is cut', () => {
    expect(gateBandH('x'.repeat(200))).toBeGreaterThan(gateBandH('short'))
  })
})
