import type { Marker } from '../viewmodel'

/** Spinner is the one moving thing: a small arc, CSS-rotated (no re-renders). */
export function Spinner({ size = 11 }: { size?: number }) {
  return (
    <svg className="spinner" width={size} height={size} viewBox="0 0 16 16" aria-label="working">
      <circle cx="8" cy="8" r="6" fill="none" strokeWidth="2.2" className="spinner-track" />
      <path d="M8 2 a6 6 0 0 1 6 6" fill="none" strokeWidth="2.2" strokeLinecap="round" className="spinner-arc" />
    </svg>
  )
}

const markerGlyph: Record<Marker, string> = {
  spin: '',
  run: '●',
  needs: '⚑',
  blocked: '✗',
  done: '✓',
  review: '⎇',
  open: '○',
}

export function StepMarker({ marker }: { marker: Marker }) {
  if (marker === 'spin') {
    return (
      <span className="mark mark-run">
        <Spinner size={10} />
      </span>
    )
  }
  return <span className={`mark mark-${marker}`}>{markerGlyph[marker]}</span>
}
