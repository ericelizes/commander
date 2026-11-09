import type { Rect } from './types'

type DomRectLike = { left: number; top: number; width: number; height: number }

// bodyRect is the native view's rect for the body element, in CSS px of the
// host page, minus an inset (so the view sits inside the pane's border).
// Fractional edges are snapped outward-in so the view never overhangs the pane.
export function bodyRect(r: DomRectLike, inset = 0): Rect | null {
  const left = Math.ceil(r.left + inset)
  const top = Math.ceil(r.top + inset)
  const right = Math.floor(r.left + r.width - inset)
  const bottom = Math.floor(r.top + r.height - inset)
  if (right - left < 1 || bottom - top < 1) return null
  return { x: left, y: top, width: right - left, height: bottom - top }
}

export function sameRect(a: Rect | null, b: Rect | null): boolean {
  if (a === null || b === null) return a === b
  return a.x === b.x && a.y === b.y && a.width === b.width && a.height === b.height
}
