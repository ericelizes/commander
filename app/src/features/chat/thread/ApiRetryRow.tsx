import { CloudOff } from 'lucide-react'
import type { ChatState } from '../events'
import { duration, useNow } from './motion'

export const STALL_HINT_MS = 2 * 60 * 1000

// The turn is stuck waiting on Anthropic API retries: one quiet row in the spot
// the thinking row would be, plus a hint once it has gone on for a while.
export function ApiRetryRow({ retry }: { retry: NonNullable<ChatState['apiRetry']> }) {
  const now = useNow(true)
  const elapsed = Math.max(0, now - retry.firstTs)
  return (
    <div className="ct-thinking ct-retry" role="status" aria-label="Anthropic API retrying">
      <CloudOff size={14} className="ct-retry-icon" />
      <span className="ct-shimmer">Anthropic API retrying (attempt {retry.attempt})</span>
      <span className="ct-elapsed">
        · {duration(elapsed)}
        {elapsed >= STALL_HINT_MS && ' · The turn may stall; your messages are queued'}
      </span>
    </div>
  )
}
