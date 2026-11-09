import type { CSSProperties } from 'react'
import { DecisionCard } from './DecisionCard'
import { HtmlCardHost } from './HtmlCardHost'
import { NeedsYouQueue } from './NeedsYouQueue'
import decisionForm from './examples/decision-form.html?raw'
import { FIXTURE_NOW_MS, fakeApi, fixtureItems, inputCard, optionsCard, stepsCard } from './fixtures'

// A fixture gallery for local screenshots and
// eyeballing: 'cards' shows the three answer modes, 'queue' the NEEDS YOU list
// beside a Manager-authored html form.
export function DecisionsGallery({ view = 'cards' }: { view?: 'cards' | 'queue' }) {
  const api = fakeApi()
  const page: CSSProperties = { minHeight: '100vh', padding: 28, background: '#11111b', boxSizing: 'border-box' }
  if (view === 'queue') {
    const pricing = { ...optionsCard, bead_id: 'example-app-9', project: 'example-app', decision: { question: 'Which pricing tier ships first?', options: [{ label: 'Starter' }, { label: 'Team' }, { label: 'Enterprise' }] } }
    return (
      <div style={{ ...page, display: 'grid', gridTemplateColumns: '340px 1fr', gap: 24, alignItems: 'start' }}>
        <NeedsYouQueue items={fixtureItems} api={api} onOpen={() => {}} now={FIXTURE_NOW_MS} />
        <div style={{ height: 470 }}>
          <HtmlCardHost html={decisionForm} title="Pricing" api={fakeApi([pricing])} allowScripts needs={pricing} />
        </div>
      </div>
    )
  }
  return (
    <div style={{ ...page, display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 24, alignItems: 'start' }}>
      <DecisionCard needs={optionsCard} api={api} autoFocus={false} />
      <DecisionCard needs={stepsCard} api={api} autoFocus={false} />
      <DecisionCard needs={inputCard} api={api} autoFocus={false} />
    </div>
  )
}
