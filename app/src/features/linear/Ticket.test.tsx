import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { useIntegrations } from './settings'
beforeEach(() => useIntegrations.setState({ linearWorkspace: 'acme' }))
afterEach(() => { cleanup(); useIntegrations.setState({ linearWorkspace: '' }) })
import { OPEN_TAB_EVENT } from '../inspector/connections'
import { LinearPill, splitTickets, TicketPills } from './Ticket'

describe('splitTickets', () => {
  it('takes ticket ids out of titles, with their parentheses, and tidies what is left', () => {
    expect(splitTickets('Sample catalog refresh (TEAM-101)')).toEqual({ text: 'Sample catalog refresh', tickets: ['TEAM-101'], prs: [] })
    expect(splitTickets('Open a PR for the sample reading list (TEAM-102)?')).toEqual({ text: 'Open a PR for the sample reading list?', tickets: ['TEAM-102'], prs: [] })
    expect(splitTickets('Get Ops sign-off on retries (TEAM-103) - who approves?').text).toBe('Get Ops sign-off on retries - who approves?')
    expect(splitTickets('TEAM-12: fix the dev flow').text).toBe('fix the dev flow')
  })

  it('leaves text without tickets alone, including look-alikes', () => {
    for (const t of ['U1 spike: cache layer', 'Merge #101 > #102', 'example-app-t03 rebase', 'AE1–AE6 checks']) {
      expect(splitTickets(t)).toEqual({ text: t, tickets: [], prs: [] })
    }
  })
})

describe('splitTickets PR numbers', () => {
  it('takes a parenthesised PR number out of the title', () => {
    expect(splitTickets('Trim the nightly report job on test hosts (#117)')).toEqual({ text: 'Trim the nightly report job on test hosts', tickets: [], prs: ['117'] })
    expect(splitTickets('Trim reports (PR #117, TEAM-101)')).toEqual({ text: 'Trim reports', tickets: ['TEAM-101'], prs: ['117'] })
  })
})

describe('TicketPills', () => {
  it('shows a GitHub pill linking the PR and a Linear pill linking the ticket', () => {
    render(<TicketPills tickets={['TEAM-101']} prs={['117']} repo="acme/example-app" />)
    expect(screen.getByRole('link', { name: /#117/ })).toHaveAttribute('href', 'https://github.com/acme/example-app/pull/117')
    expect(screen.getByRole('link', { name: /TEAM-101/ })).toBeTruthy()
  })

  it('opens a clicked pill as an inspector tab, not the browser', () => {
    const opened: unknown[] = []
    const on = (e: Event) => opened.push((e as CustomEvent).detail)
    window.addEventListener(OPEN_TAB_EVENT, on)
    render(<TicketPills tickets={['TEAM-101']} prs={['117']} repo="acme/example-app" />)
    expect(fireEvent.click(screen.getByRole('link', { name: /#117/ }))).toBe(false) // default prevented
    fireEvent.click(screen.getByRole('link', { name: /TEAM-101/ }))
    window.removeEventListener(OPEN_TAB_EVENT, on)
    expect(opened).toEqual([
      { url: 'https://github.com/acme/example-app/pull/117', title: '#117' },
      { url: 'https://linear.app/acme/issue/TEAM-101', title: 'TEAM-101' },
    ])
  })

  it('renders an unlinked PR pill without a repo', () => {
    render(<TicketPills tickets={[]} prs={['117']} />)
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByTitle('PR #117')).toBeTruthy()
  })
})

describe('LinearPill', () => {
  it('keeps unconfigured tickets visible without linking another workspace', () => {
    useIntegrations.setState({ linearWorkspace: '' })
    render(<LinearPill id="ACME-1" />)
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByText('ACME-1')).toBeTruthy()
  })
  it('uses a different configured workspace', () => {
    useIntegrations.setState({ linearWorkspace: 'example-team' })
    render(<LinearPill id="ACME-1" />)
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://linear.app/example-team/issue/ACME-1')
  })
  it('links to the ticket in Linear', () => {
    render(<LinearPill id="TEAM-101" />)
    expect(screen.getByRole('link', { name: /TEAM-101/ })).toHaveAttribute('href', 'https://linear.app/acme/issue/TEAM-101')
  })
})

it('takes long keys and underscore-bounded ids out of a title, and leaves lowercase words', () => {
  expect(splitTickets('Move PLATFORM-12 now')).toMatchObject({ text: 'Move now', tickets: ['PLATFORM-12'] })
  expect(splitTickets('Move (PLATFORM-12, #4)')).toMatchObject({ text: 'Move', tickets: ['PLATFORM-12'], prs: ['4'] })
  expect(splitTickets('op_TEAM-12').tickets).toEqual(['TEAM-12'])
  expect(splitTickets('fix utf-8 team-4321')).toMatchObject({ text: 'fix utf-8 team-4321', tickets: [] })
})
