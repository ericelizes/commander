// MachineChart is one machine reading (memory, disk or CPU) over the last 30
// minutes as a line on a fixed 0-100% scale, with the current value and what
// is left beside the title, and a crosshair tooltip on hover.

import { useRef, useState } from 'react'

export interface Point {
  at: number // unix seconds
  pct: number
}

const W = 320
const H = 72
const PAD_TOP = 4
const PAD_BOTTOM = 4

export function MachineChart({ name, points, now, detail }: { name: string; points: Point[]; now: number; detail: string }) {
  const [hover, setHover] = useState<number | null>(null)
  const ref = useRef<SVGSVGElement>(null)
  const start = now - 30 * 60
  const x = (at: number) => ((at - start) / (now - start)) * W
  const y = (pct: number) => PAD_TOP + (1 - pct / 100) * (H - PAD_TOP - PAD_BOTTOM)
  const shown = points.filter((p) => p.at >= start)
  const path = shown.map((p, i) => `${i ? 'L' : 'M'}${x(p.at).toFixed(1)},${y(p.pct).toFixed(1)}`).join(' ')
  const last = shown[shown.length - 1]
  const tone = !last ? '' : last.pct >= 90 ? ' is-critical' : last.pct >= 75 ? ' is-warning' : ''
  const onMove = (e: React.MouseEvent) => {
    const r = ref.current?.getBoundingClientRect()
    if (!r || !shown.length) return
    const at = start + ((e.clientX - r.left) / r.width) * (now - start)
    let best = 0
    for (let i = 1; i < shown.length; i++) if (Math.abs(shown[i].at - at) < Math.abs(shown[best].at - at)) best = i
    setHover(best)
  }
  const h = hover === null ? null : shown[hover]
  return (
    <figure className={`machine-chart${tone}`}>
      <figcaption>
        <span className="machine-chart-name">{name}</span>
        <span className="machine-chart-val">{last ? `${Math.round(last.pct)}%` : '—'}</span>
        <span className="machine-chart-detail">{detail}</span>
      </figcaption>
      <div className="machine-chart-plot">
        <svg ref={ref} viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-label={`${name} over the last 30 minutes`} onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
          <line className="machine-chart-grid" x1={0} x2={W} y1={y(50)} y2={y(50)} vectorEffect="non-scaling-stroke" />
          <line className="machine-chart-grid" x1={0} x2={W} y1={y(100)} y2={y(100)} vectorEffect="non-scaling-stroke" />
          <line className="machine-chart-base" x1={0} x2={W} y1={y(0)} y2={y(0)} vectorEffect="non-scaling-stroke" />
          {path && <path className="machine-chart-line" d={path} vectorEffect="non-scaling-stroke" />}
          {h && <line className="machine-chart-cross" x1={x(h.at)} x2={x(h.at)} y1={0} y2={H} vectorEffect="non-scaling-stroke" />}
        </svg>
        {last && <span className="machine-chart-dot" style={{ left: `${(x(last.at) / W) * 100}%`, top: `${(y(last.pct) / H) * 100}%` }} />}
        {h && (
          <span className="machine-chart-tip" style={{ left: `${(x(h.at) / W) * 100}%` }}>
            {Math.round(h.pct)}% · {ago(now - h.at)}
          </span>
        )}
      </div>
      <div className="machine-chart-axis" aria-hidden>
        <span>30 min ago</span>
        <span>now</span>
      </div>
    </figure>
  )
}

function ago(sec: number): string {
  if (sec < 5) return 'now'
  if (sec < 60) return `${Math.round(sec)}s ago`
  return `${Math.round(sec / 60)} min ago`
}
