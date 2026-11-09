// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, expect, it } from 'vitest'
import fixture from '../../internal/livedata/testdata/board/stale-words.json'
import display from '../../internal/livedata/testdata/board/stale-words.want.json'
import type { Snapshot } from '../src/shared/snapshot'
import { deriveSnapshot } from '../src/renderer/src/viewmodel'
import { NeedsYouQueue } from '../src/features/decisions/NeedsYouQueue'
import { DecisionCard } from '../src/features/decisions/DecisionCard'
import { fakeApi } from '../src/features/decisions/fixtures'
import { PitchView } from '../src/renderer/src/components/PitchView'
import { DerivedDiagram } from '../src/renderer/src/components/DerivedDiagram'
import { useStore } from '../src/renderer/src/store'

afterEach(cleanup)

it('carries server warnings into the queue and card, while a fresh card stays unmarked', () => {
 const snapshot = deriveSnapshot({ ...fixture.snapshot, display } as unknown as Snapshot)
 const items = snapshot.needs_you
 expect(items[3].stale_why).toBeUndefined()
 const queue = render(<NeedsYouQueue items={items} api={fakeApi(items)} onOpen={() => {}} />)
 // A spec edit is the Manager's to re-check, never a label; age and push stay.
 expect(items[0].stale_why).toBeUndefined()
 expect(screen.queryByText(/The spec changed/)).toBeNull()
 for (const item of items.slice(1, 3)) expect(screen.getByText(item.stale_why!)).toBeInTheDocument()
 queue.unmount()
 const card = render(<DecisionCard needs={items[1]} api={fakeApi(items)} />)
 expect(screen.getByText(items[1].stale_why!)).toBeInTheDocument()
 card.unmount()
 useStore.setState({ snapshot, config: null })
 render(<PitchView item={items[2]} />)
 expect(screen.getByText(items[2].stale_why!)).toBeInTheDocument()
})

it('keeps each server reason visible on a diagram approval', () => {
 const reasons = display.asks.slice(0, 3).map(a => a.stale_why).filter((w): w is string => !!w)
 render(<DerivedDiagram diagram={{ id: 'p-w', title: 'Stale cards', steps: reasons.map((why, i) => ({ id: `p-w.${i}`, title: `Approval ${i}`, kind: 'needs', text: `Needs you · ${why}`, actor: 'you', lane: 'you', gate: true })) }} />)
 for (const why of reasons) expect(screen.getByText((text) => text.endsWith(`Needs your approval · ${why}`))).toBeInTheDocument()
})
