// ReviewChecks is a plugged review block's checks, collapsed to a count until
// clicked: each check's title, its state in the board's words and what its
// check found (on which PR head, stale after a new push). Every word comes
// from cmdrd's review block; nothing is derived here.

import { useState } from 'react'
import type { ReviewBlock } from '../../../shared/snapshot'

const short = (h?: string) => (h && h.length > 7 ? h.slice(0, 7) : (h ?? ''))

export function ReviewChecks({ review }: { review: ReviewBlock }) {
  const [open, setOpen] = useState(false)
  const n = review.checks.length
  return (
    <div className="rv" onClick={(e) => e.stopPropagation()}>
      <button type="button" className="rv-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        <span className="wf-caret" aria-hidden>
          {open ? '▾' : '▸'}
        </span>
        {open ? 'Hide the checks' : `Show ${n} ${n === 1 ? 'check' : 'checks'}`}
      </button>
      {open && (
        <ul className="rv-list" aria-label="Checks">
          {review.checks.map((c) => (
            <li key={c.step} className={`rv-check rv-kind-${c.kind}${c.stale ? ' is-stale' : ''}`} title={c.id || undefined}>
              <span className="rv-title">{c.title}</span>
              <span className="rv-text">{c.text}</span>
              {c.finding ? (
                <span className="rv-found">
                  Found: {c.finding}
                  {c.head && <span className="rv-head"> · on {short(c.head)}</span>}
                </span>
              ) : (
                c.verifier && <span className="rv-found">Check: {c.verifier}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
