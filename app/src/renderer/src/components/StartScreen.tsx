// The one screen for "Commander isn't here yet": while the app first loads,
// and while its background service restarts. It replaces scattered error
// banners and per-chat notices with a calm screen and a counter.

import { useEffect, useRef, useState } from 'react'
import { useStore } from '../store'
import { BoatSketch } from './BoatSketch'
import { useUpdateStore } from '../updateApi'

// A blip shorter than this never shows the screen.
const GRACE_MS = 600
// After this long it says it is still trying, not just "restarting".
const SLOW_MS = 45_000

/** useAppDown is whether Commander isn't here yet: no data, or cmdrd unreachable. */
export function useAppDown(): boolean {
  return useStore((s) => !s.snapshot || s.conn !== 'live')
}

export function StartScreen() {
  const conn = useStore((s) => s.conn)
  const loaded = useStore((s) => !!s.snapshot)
  const wasLive = useRef(false)
  if (conn === 'live') wasLive.current = true
  const down = !loaded || conn !== 'live'
  const [since, setSince] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())

  useEffect(() => {
    if (!down) {
      setSince(null)
      return
    }
    const start = Date.now()
    setSince(start)
    setNow(start)
    const t = setInterval(() => setNow(Date.now()), 250)
    return () => clearInterval(t)
  }, [down])

  const elapsed = since === null ? 0 : now - since
  // An open update dialog shows the restart itself (it grows to fill the window).
  const updating = useUpdateStore((s) => s.panelOpen)
  const show = down && (!wasLive.current || elapsed >= GRACE_MS) && !(wasLive.current && updating)
  const restarting = wasLive.current
  const secs = Math.floor(elapsed / 1000)

  return (
    <div className={`start-screen${show ? ' on' : ''}`} role="status" aria-live="polite" aria-hidden={!show}>
      <div className="start-drag drag" />
      <div className="start-mark" aria-hidden>
        <BoatSketch />
      </div>
      <div className="start-title">{restarting ? 'Restarting…' : 'Starting…'}</div>
      <div className="start-sub">
        {elapsed > SLOW_MS ? `Still trying · ${secs}s. Your chats keep running.` : secs > 0 ? `${secs}s` : ' '}
      </div>
    </div>
  )
}

