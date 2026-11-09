// Small timing and motion helpers shared by the thread.

import { useEffect, useRef, useState } from 'react'

export function duration(ms: number) {
  const seconds = Math.max(0, Math.round(ms / 1000))
  if (seconds < 60) return `${seconds}s`
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
}

/** useNow re-renders once a second while `live`, for elapsed counters. */
export function useNow(live: boolean) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!live) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [live])
  return now
}

const FADE_MS = 260

/** SwapText crossfades from one label to the next, with no blank frame between them. */
// What a running step shows stays up at least this long: steps can change
// many times a second, and a swap per step reads as flicker.
export const HOLD_MS = 900

/**
 * useHeld returns value, but changes it at most once per HOLD_MS: changes in
 * between are skipped (the latest wins), not queued. key says when it changed.
 */
export function useHeld<T>(value: T, key: string): T {
  const [held, setHeld] = useState({ value, key })
  const shownAt = useRef(0)
  const latest = useRef({ value, key })
  latest.current = { value, key }
  useEffect(() => {
    if (key === held.key) return
    const wait = Math.max(0, shownAt.current + HOLD_MS - Date.now())
    const timer = window.setTimeout(() => {
      setHeld(latest.current)
      shownAt.current = Date.now()
    }, wait)
    return () => window.clearTimeout(timer)
  }, [key, held.key])
  return held.value
}

export function SwapText({ text, className = '' }: { text: string; className?: string }) {
  const [current, setCurrent] = useState(text)
  const [outgoing, setOutgoing] = useState<string | null>(null)

  // Switched during render, so the new label paints in the same frame as the icon beside it.
  if (text !== current) {
    setOutgoing(current)
    setCurrent(text)
  }

  useEffect(() => {
    if (outgoing === null) return
    const timer = window.setTimeout(() => setOutgoing(null), FADE_MS)
    return () => window.clearTimeout(timer)
  }, [outgoing])

  return (
    <span className="ct-swap">
      {outgoing && (
        <span key={`out-${outgoing}`} aria-hidden="true" className={`ct-swap-out ${className}`}>
          {outgoing}
        </span>
      )}
      <span key={`in-${current}`} className={`${outgoing ? 'ct-swap-in' : ''} ${className}`}>
        {current}
      </span>
    </span>
  )
}
