import { cleanup, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FIXTURE_NOW_MS, fakeApi, fixtureItems, optionsCard, plainFlag, stepsCard } from './fixtures'
import { NeedsYouBadge, NeedsYouQueue } from './NeedsYouQueue'
import { needsYouCount } from './logic'

afterEach(cleanup)

function setup(items = fixtureItems) {
  const api = fakeApi(items)
  const onOpen = vi.fn()
  const user = userEvent.setup()
  const view = render(<NeedsYouQueue items={items} api={api} onOpen={onOpen} now={FIXTURE_NOW_MS} />)
  return { api, onOpen, user, ...view }
}

describe('NeedsYouQueue', () => {
  it('lists question, workstream, rank and age, with a badge count', () => {
    setup()
    const options = screen.getAllByRole('option')
    expect(options).toHaveLength(4)
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    const first = within(options[0])
    expect(first.getByText('Where should import events be stored?')).toBeInTheDocument()
    expect(first.getByText('Import pipeline')).toBeInTheDocument()
    expect(first.getByTitle(`Workstream 1`)).toHaveTextContent('1')
    expect(first.getByText('42m')).toBeInTheDocument()
    expect(within(options[1]).getByText('3h')).toBeInTheDocument()
    expect(within(options[2]).getByText('2d')).toBeInTheDocument()
    expect(within(options[3]).getByText('Verify: dmg opens on your Mac')).toBeInTheDocument()
    expect(screen.getByLabelText('4 waiting on you')).toHaveTextContent('4')
  })

  it('moves with arrows/j/k and opens with Enter or a click', async () => {
    const { user, onOpen } = setup()
    screen.getByRole('listbox').focus()
    await user.keyboard('{ArrowDown}j{ArrowUp}')
    expect(screen.getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('listbox').getAttribute('aria-activedescendant')).toBe(screen.getAllByRole('option')[1].id)
    await user.keyboard('{Enter}')
    expect(onOpen).toHaveBeenLastCalledWith(stepsCard)
    await user.keyboard('{End}{Home}')
    expect(screen.getAllByRole('option')[0]).toHaveAttribute('aria-selected', 'true')

    await user.click(screen.getByText('Verify: dmg opens on your Mac'))
    expect(onOpen).toHaveBeenLastCalledWith(plainFlag)
  })

  it('approves a plain flag with a and removes it', async () => {
    const { user, api } = setup()
    screen.getByRole('listbox').focus()
    await user.keyboard('{End}a')
    expect(await screen.findByText('Approved: Verify: dmg opens on your Mac')).toBeInTheDocument()
    expect(api.calls).toEqual([['internal-resolve-needs', '--project', 'commander', '--bead', 'cmdr-31', '--action', 'approve']])
    expect(screen.getAllByRole('option')).toHaveLength(3)
    expect(screen.getAllByRole('option')[2]).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByLabelText('3 waiting on you')).toBeInTheDocument()
  })

  it('defers a plain flag with d', async () => {
    const { user, api } = setup([plainFlag])
    screen.getByRole('listbox').focus()
    await user.keyboard('d')
    await screen.findByText('All clear')
    expect(api.calls[0]).toEqual(['internal-resolve-needs', '--project', 'commander', '--bead', 'cmdr-31', '--action', 'defer'])
  })

  it('does not approve or defer a decision card from the list', async () => {
    const { user, api } = setup()
    screen.getByRole('listbox').focus()
    await user.keyboard('a')
    await user.keyboard('d')
    expect(api.calls).toHaveLength(0)
    expect(screen.getByText(/has a decision card: press Enter/)).toBeInTheDocument()
  })

  it('shows an error on the row and keeps it', async () => {
    const api = fakeApi([plainFlag])
    api.next = { ok: false, exit: 1, stderr: '{"error":"no such bead"}' }
    const user = userEvent.setup()
    render(<NeedsYouQueue items={[plainFlag]} api={api} onOpen={() => {}} />)
    screen.getByRole('listbox').focus()
    await user.keyboard('a')
    expect(await screen.findByText('✗ no such bead')).toBeInTheDocument()
    expect(screen.getAllByRole('option')).toHaveLength(1)
  })

  it('has an empty state and no badge', () => {
    setup([])
    expect(screen.getByText('All clear')).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/waiting on you/)).not.toBeInTheDocument()
  })

  it('follows new snapshots', () => {
    const api = fakeApi()
    const { rerender } = render(<NeedsYouQueue items={[optionsCard]} api={api} onOpen={() => {}} />)
    rerender(<NeedsYouQueue items={[optionsCard, plainFlag]} api={api} onOpen={() => {}} />)
    expect(screen.getAllByRole('option')).toHaveLength(2)
  })
})

describe('badge helpers', () => {
  it('counts the snapshot and caps the badge', () => {
    expect(needsYouCount({ needs_you: fixtureItems })).toBe(4)
    expect(needsYouCount(null)).toBe(0)
    render(<NeedsYouBadge count={120} />)
    expect(screen.getByLabelText('120 waiting on you')).toHaveTextContent('99+')
  })
})

describe('NeedsYouQueue collapse', () => {
  it('folds the list away from its head and opens again', async () => {
    const { rerender } = render(<NeedsYouQueue items={fixtureItems} api={{} as never} onOpen={() => {}} collapsed onToggle={() => {}} />)
    expect(screen.queryByRole('listbox')).toBeNull()
    expect(screen.getByRole('button', { name: /Needs you/ }).getAttribute('aria-expanded')).toBe('false')
    rerender(<NeedsYouQueue items={fixtureItems} api={{} as never} onOpen={() => {}} collapsed={false} onToggle={() => {}} />)
    expect(screen.getByRole('listbox')).toBeTruthy()
  })
})
