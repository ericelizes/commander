// A small modal for actions that change who owns an agent's session.
// Esc or the backdrop cancels; the confirm button has focus.

import { useEffect, useRef } from 'react'

export function ConfirmDialog({
  title,
  confirm,
  onConfirm,
  onCancel,
  children,
}: {
  title: string
  confirm: string
  onConfirm: () => void
  onCancel: () => void
  children: React.ReactNode
}) {
  const ok = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    ok.current?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onCancel()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onCancel])
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}>
        <h3>{title}</h3>
        <div className="modal-body">{children}</div>
        <div className="modal-actions">
          <button type="button" className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" className="btn btn-primary" ref={ok} onClick={onConfirm}>
            {confirm}
          </button>
        </div>
      </div>
    </div>
  )
}
