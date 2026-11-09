import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { PrPill } from './PrPill'

afterEach(cleanup)

describe('PrPill', () => {
  it('links a PR to GitHub when the repo is known', () => {
    render(<PrPill repo="acme/example-app" n="4107" />)
    expect(screen.getByRole('link', { name: /#4107/ })).toHaveAttribute('href', 'https://github.com/acme/example-app/pull/4107')
  })

  it('is an unlinked pill without a repo', () => {
    render(<PrPill n="#12" />)
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByTitle('PR #12')).toHaveTextContent('#12')
  })
})
