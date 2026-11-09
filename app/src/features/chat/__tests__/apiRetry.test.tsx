import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiRetryRow } from '../thread/ApiRetryRow'

describe('ApiRetryRow', () => {
  afterEach(() => vi.useRealTimers())

  it('shows the attempt and time since the first retry, and a stall hint after two minutes', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-22T09:50:00Z'))
    const firstTs = Date.now() - 5000
    const { rerender } = render(<ApiRetryRow retry={{ attempt: 3, firstTs, ts: Date.now() }} />)
    expect(screen.getByText('Anthropic API retrying (attempt 3)')).toBeTruthy()
    expect(screen.getByText('· 5s')).toBeTruthy()
    expect(screen.queryByText(/may stall/)).toBeNull()
    rerender(<ApiRetryRow retry={{ attempt: 9, firstTs: Date.now() - 130_000, ts: Date.now() }} />)
    expect(screen.getByText(/The turn may stall; your messages are queued/)).toBeTruthy()
  })
})
