import { useCallback, useRef } from 'react'

/** Splitter drags a column edge; side says which way a positive drag grows the column. */
export function Splitter({ onDrag, onDone, side }: { onDrag(dx: number): void; onDone(): void; side: 'left' | 'right' }) {
  const start = useRef(0)
  const onPointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      e.currentTarget.setPointerCapture(e.pointerId)
      start.current = e.clientX
      document.body.classList.add('resizing')
    },
    [],
  )
  const onPointerMove = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      if (!e.currentTarget.hasPointerCapture(e.pointerId)) return
      const dx = e.clientX - start.current
      start.current = e.clientX
      onDrag(side === 'left' ? dx : -dx)
    },
    [onDrag, side],
  )
  const onPointerUp = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      e.currentTarget.releasePointerCapture(e.pointerId)
      document.body.classList.remove('resizing')
      onDone()
    },
    [onDone],
  )
  return <div className="splitter" role="separator" aria-orientation="vertical" onPointerDown={onPointerDown} onPointerMove={onPointerMove} onPointerUp={onPointerUp} />
}
