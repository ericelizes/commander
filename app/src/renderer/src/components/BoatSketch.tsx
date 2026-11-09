// A line sketch of a sailboat that draws itself in, then rides passing waves.
// The waiting graphic for restarts and updates. Setting `arrived` on the same
// instance sails the boat to a lighthouse and moors it: only the lighthouse
// and the jetty draw in, the boat is never redrawn.

import { useEffect, useId, useState } from 'react'

const WAVE = (y: number, h: number) => `M-20 ${y} q10 -${h} 20 0` + ' t20 0'.repeat(13)

export function BoatSketch({ size = 220, arrived = false }: { size?: number; arrived?: boolean }) {
  // Ids per instance: the start screen keeps a hidden copy mounted, and a
  // shared mask id would resolve to that invisible one and blank this one.
  const id = useId()
  // Once moored (the glide and the harbour drawing take ~2s) everything holds
  // still: paused where it is, so nothing jumps.
  const [still, setStill] = useState(false)
  useEffect(() => {
    if (!arrived) return setStill(false)
    const t = setTimeout(() => setStill(true), 2200)
    return () => clearTimeout(t)
  }, [arrived])
  return (
    <svg className={`boat${arrived ? ' boat-arrived' : ''}${still ? ' boat-still' : ''}`} viewBox="0 0 200 120" width={size} height={(size * 120) / 200} fill="none" aria-hidden>
      <defs>
        <linearGradient id={`${id}fade`}>
          <stop offset="0" stopColor="#fff" stopOpacity="0" />
          <stop offset="0.2" stopColor="#fff" />
          <stop offset="0.8" stopColor="#fff" />
          <stop offset="1" stopColor="#fff" stopOpacity="0" />
        </linearGradient>
        <mask id={`${id}edges`}>
          <rect width="200" height="120" fill={`url(#${id}fade)`} />
        </mask>
      </defs>
      <g mask={`url(#${id}edges)`}>
        <circle className="boat-sun boat-draw boat-d4" cx="158" cy="28" r="10" pathLength={1} />
        {arrived && (
          <g className="boat-harbour">
            <path className="boat-draw boat-d2" pathLength={1} d="M152 86 L156 46 L170 46 L174 86" />
            <path className="boat-faint" d="M155 62 L171 62 M154 74 L172 74" />
            <path className="boat-draw boat-d3" pathLength={1} d="M156 46 L156 38 L170 38 L170 46" />
            <path className="boat-draw boat-d4" pathLength={1} d="M153 38 L163 29 L173 38" />
            <path className="boat-beam" d="M174 41 L196 34 M174 43 L196 50" />
            <path className="boat-draw boat-d2" pathLength={1} d="M114 84 H152 M122 84 V97 M134 84 V97 M146 84 V97" />
          </g>
        )}
        <g className="boat-sail">
          <g className="boat-ship">
            <path className="boat-draw" pathLength={1} d="M58 78 L142 78 Q136 90 126 92 L74 92 Q64 90 58 78 Z" />
            <path className="boat-draw boat-d2" pathLength={1} d="M100 78 L100 22" />
            <path className="boat-draw boat-d2" pathLength={1} d="M103 26 Q126 46 130 72 L103 72 Z" />
            <path className="boat-draw boat-d3" pathLength={1} d="M97 32 Q80 50 74 72 L97 72 Z" />
            <path className="boat-draw boat-d4" pathLength={1} d="M100 22 L112 26 L100 30" />
          </g>
        </g>
        <path className="boat-wave boat-w1" d={WAVE(90, 6) + ' V130 H-20 Z'} />
        <path className="boat-wave boat-w2" d={WAVE(104, 5)} />
      </g>
    </svg>
  )
}
