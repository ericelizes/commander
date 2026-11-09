import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { DegradedBanner } from './DegradedBanner'

afterEach(cleanup)

describe('DegradedBanner', () => {
  it('shows the reason as an alert when the snapshot is degraded', () => {
    render(<DegradedBanner degraded={{ reason: 'schema 11 is newer than this binary supports' }} />)
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('State unreadable')
    expect(alert.textContent).toContain('schema 11 is newer')
  })

  it('lists health problems that keep failing, first line each', () => {
    render(<DegradedBanner degraded={undefined} health={[{ key: 'mail:manager-x', detail: "Mail can't reach manager-x: its chat could not be started\nmore", since: 1793300000 }]} />)
    const alert = screen.getByRole('alert')
    expect(alert.textContent).toContain('Something is not working')
    expect(alert.textContent).toContain("Mail can't reach manager-x")
    expect(alert.textContent).not.toContain('more')
  })

  it('renders nothing when state is readable', () => {
    render(<DegradedBanner degraded={undefined} />)
    expect(screen.queryByRole('alert')).toBeNull()
  })
})
