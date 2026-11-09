// A stage's gates as a checklist: passed has a check and its evidence on hover,
// failed a cross, open a hollow circle, each with its verifier; a workstream
// with no gates says so quietly; the graph makes room for the list.
import { cleanup, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import type { Snapshot } from '../src/shared/snapshot'
import { GateChecklist } from '../src/renderer/src/components/GateChecklist'
import { buildCanvas, CARD_H, GATE_ROW } from '../src/renderer/src/graph'
import { wsKey } from '../src/renderer/src/viewmodel'

const snap = JSON.parse(readFileSync(join(__dirname, 'fixtures/snapshot.json'), 'utf8')) as Snapshot

afterEach(cleanup)

describe('GateChecklist', () => {
  it('marks passed, failed and open gates, with the verifier and the evidence on hover', () => {
    render(<GateChecklist gates={snap.workstreams[0].steps[0].gates!} />)
    const items = screen.getAllByRole('listitem')
    expect(items.map((i) => i.textContent)).toEqual([
      '✓Spec reviewedreviewer:reviewer-a',
      '✗Tests greenauto',
      '○the user signs offuser',
    ])
    expect(items[0].title).toBe('Spec reviewed: Approved in review, 2 comments resolved')
    expect(items[1].title).toContain('2 failures')
  })
})

describe('gates in the view model and the graph', () => {
  it('makes a gated card taller by one line per gate', () => {
    const ws = snap.workstreams[0]
    const card = buildCanvas(snap, wsKey(ws)).cards.find((c) => c.gates)!
    expect(card.h).toBe(CARD_H + 3 * GATE_ROW)
  })
})
