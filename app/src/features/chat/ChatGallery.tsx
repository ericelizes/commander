// Dev/screenshot gallery: renders a fixture conversation in a ChatPane over a
// ReplayTransport. Mount it from any page; pick the fixture with #<name>.
import { useMemo } from 'react'
import { ChatPane } from './ChatPane'
import { FIXTURES, type FixtureName } from './fixtures'
import { ReplayTransport } from './transport'
import type { ChatEvent } from './events'

export function ChatGallery({ name, events, title }: { name?: string; events?: ChatEvent[]; title?: string }) {
  const fixture = (name ?? (typeof location !== 'undefined' ? location.hash.slice(1) : '')) || 'longAnswer'
  const transport = useMemo(() => {
    const evs = events ?? (FIXTURES[fixture as FixtureName] ?? FIXTURES.longAnswer)()
    return new ReplayTransport(evs, { echo: true })
  }, [fixture, events])
  return (
    <div style={{ height: '100vh' }}>
      <ChatPane role={`gallery-${fixture}`} title={title ?? 'Operator · bd-142 snapshot ranks'} transport={transport} />
    </div>
  )
}
