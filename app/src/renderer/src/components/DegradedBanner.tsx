import type { Snapshot } from '../../../shared/snapshot'

// Red strip above the navigator while something is wrong that the rows below
// would not show: cmdrd cannot read its state (everything is stale), or a
// health problem keeps failing (mail cannot reach an agent, a plan breaks the
// rules). It clears on its own when the problem does.
export function DegradedBanner({ degraded, health }: { degraded: Snapshot['degraded']; health?: Snapshot['health'] }) {
  if (!degraded && !health?.length) return null
  return (
    <div className="nav-degraded" role="alert">
      {degraded && (
        <>
          <strong>State unreadable</strong>
          <span>{degraded.reason}</span>
        </>
      )}
      {!!health?.length && (
        <>
          <strong>{health.length === 1 ? 'Something is not working' : `${health.length} things are not working`}</strong>
          {health.map((h) => (
            <span key={h.key} title={h.detail}>
              {h.detail.split('\n')[0]} <span className="nav-degraded-since">since {since(h.since)}</span>
            </span>
          ))}
        </>
      )}
    </div>
  )
}

function since(unix: number): string {
  return new Date(unix * 1000).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
}
