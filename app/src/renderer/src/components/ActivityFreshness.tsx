import type { Snapshot } from '../../../shared/snapshot'
import { useStore, type Conn } from '../store'

// Independent of content version, generated_at and WebSocket connectivity.
// Three missed 15-second clock ticks allow for a bounded source read.
export const ACTIVITY_STALE_SECONDS = 45
export function activityUnverified(snapshot: Snapshot | null, conn: Conn, nowSec: number): boolean {
  if (!snapshot) return false
  const completed = Date.parse(snapshot.load_completed_at ?? '') / 1000
  return conn !== 'live' || !!snapshot.degraded || !!snapshot.error || !!snapshot.load_error ||
    !Number.isFinite(completed) || nowSec - completed >= ACTIVITY_STALE_SECONDS
}

export function ActivityFreshness() {
  const snapshot = useStore((s) => s.snapshot)
  const conn = useStore((s) => s.conn)
  const now = useStore((s) => s.nowSec)
  if (!activityUnverified(snapshot, conn, now)) return null
  return <p role="status" className="activity-freshness">Current activity unverified. Waiting for a fresh successful load.</p>
}
