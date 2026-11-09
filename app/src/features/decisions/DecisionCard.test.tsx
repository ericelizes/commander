import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup } from '@testing-library/react'
import { DecisionCard } from './DecisionCard'
import { fakeApi, inputCard, optionsCard, plainFlag, stepsCard } from './fixtures'

afterEach(cleanup)

const base = (bead: string) => ['internal-resolve-needs', '--project', expect.any(String), '--bead', bead]

describe('DecisionCard: options', () => {
  it('is a keyboard-only flow: focus starts on option 1, a digit picks, Enter submits', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    const onDone = vi.fn()
    render(<DecisionCard needs={optionsCard} api={api} onDone={onDone} />)

    expect(screen.getByRole('heading', { level: 2, name: 'Where should import events be stored?' })).toBeInTheDocument()
    expect(screen.getByText(/Imports are written today/)).toBeInTheDocument()
    expect(screen.getByText(/Both options keep the nightly job/)).toBeInTheDocument()
    const radios = screen.getAllByRole('radio')
    expect(radios).toHaveLength(3)
    expect(radios[0]).toHaveFocus()
    expect(within(radios[0]).getByText('★ Recommended')).toBeInTheDocument()
    expect(radios[1]).toHaveAccessibleDescription(/New topic and a consumer service/)
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()

    await user.keyboard('2')
    expect(radios[1]).toHaveAttribute('aria-checked', 'true')
    expect(radios[1]).toHaveFocus()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeEnabled()

    await user.keyboard('{Enter}')
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls).toEqual([[...base('imports-12.3'), '--action', 'decide', '--choice', 'Kafka topic + consumer']])
    expect(screen.getByText('Kafka topic + consumer')).toBeInTheDocument()
    expect(screen.getByText('✓ Answered')).toBeInTheDocument()
    expect(onDone).toHaveBeenCalledWith({ kind: 'answered', action: 'decide', choice: 'Kafka topic + consumer', note: undefined })
  })

  it('moves with arrows, takes a note, and submits with Cmd+Enter from the note', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowUp}')
    expect(screen.getAllByRole('radio')[1]).toHaveAttribute('aria-checked', 'true')
    await user.keyboard('n')
    const note = screen.getByLabelText(/Add a note/)
    expect(note).toHaveFocus()
    await user.keyboard('after the audit sync')
    await user.keyboard('{Meta>}{Enter}{/Meta}')
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls[0]).toEqual([...base('imports-12.3'), '--action', 'decide', '--choice', 'Kafka topic + consumer', '--text', 'after the audit sync'])
    expect(screen.getByText('after the audit sync')).toBeInTheDocument()
  })

  it('works by mouse, and Enter without a pick only warns', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('{Enter}')
    expect(api.calls).toHaveLength(0)
    expect(screen.getByText('Pick an option (1–3)')).toHaveClass('is-warn')

    await user.click(screen.getByText('Postgres outbox table'))
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls[0]).toContain('Postgres outbox table')
  })

  it('shows the error and lets the user retry', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    api.next = { ok: false, exit: 1, stderr: '{"error":"br: database is locked"}\n' }
    render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('1{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent('br: database is locked')
    expect(screen.getAllByRole('radio')[0]).toHaveAttribute('aria-checked', 'true')

    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls).toHaveLength(2)
  })

  it('reports a network failure', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    api.next = new Error('cmdrd unreachable')
    render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('3{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent('cmdrd unreachable')
  })

  it('disables everything while sending', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    let release!: () => void
    const gate = new Promise<void>(r => { release = r })
    const run = api.runCmd
    api.runCmd = async argv => { await gate; return run(argv) }
    render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('1{Enter}')
    expect(await screen.findByRole('button', { name: 'Sending…' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Defer' })).toBeDisabled()
    release()
    await screen.findByRole('heading', { name: 'Answer sent' })
  })
})

describe('DecisionCard: secondary actions', () => {
  it('defer and approve map to --action defer|approve', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    const { unmount } = render(<DecisionCard needs={optionsCard} api={api} />)
    await user.click(screen.getByRole('button', { name: 'Defer' }))
    await screen.findByRole('heading', { name: 'Deferred' })
    expect(api.calls[0]).toEqual([...base('imports-12.3'), '--action', 'defer'])
    unmount()

    render(<DecisionCard needs={inputCard} api={api} />)
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    await screen.findByRole('heading', { name: 'Approved' })
    expect(api.calls[1]).toEqual([...base('example-app-7'), '--action', 'approve'])
  })
})

describe('DecisionCard: steps only', () => {
  it('copies one command or all of them, and Done is ready at once', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    render(<DecisionCard needs={stepsCard} api={api} />)
    const done = screen.getByRole('button', { name: 'Done' })
    expect(done).toBeEnabled()
    expect(done).toHaveFocus()
    expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument() // Done is the answer

    await user.click(screen.getByRole('button', { name: 'Copy command 2' }))
    expect(api.copied).toEqual(['just db-migrate env=staging'])
    expect(await screen.findByRole('status')).toHaveTextContent('Copied command')

    await user.click(screen.getByRole('button', { name: 'Copy all' }))
    expect(api.copied[1]).toBe(stepsCard.decision!.steps!.join('\n'))

    screen.getByRole('region').focus()
    await user.keyboard('c')
    expect(api.copied).toHaveLength(3)

    await user.click(done)
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls[0]).toEqual([...base('imports-14.1'), '--action', 'decide', '--choice', 'done'])
  })

  it('reports a copy failure in the toast', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    api.copy = async () => { throw new Error('denied') }
    render(<DecisionCard needs={stepsCard} api={api} />)
    await user.click(screen.getByRole('button', { name: 'Copy command 1' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Copy failed: denied')
  })
})

describe('DecisionCard: free text', () => {
  it('needs an answer before it can send', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    render(<DecisionCard needs={inputCard} api={api} />)
    const answer = screen.getByLabelText('Your answer')
    expect(answer).toHaveFocus()
    expect(answer).toBeRequired()
    const send = screen.getByRole('button', { name: 'Send answer' })
    expect(send).toBeDisabled()
    expect(send).toHaveAccessibleDescription('Write an answer first')

    await user.type(answer, 'example-app Open')
    expect(send).toBeEnabled()
    await user.click(send)
    await screen.findByRole('heading', { name: 'Answer sent' })
    expect(api.calls[0]).toEqual([...base('example-app-7'), '--action', 'decide', '--choice', 'answered', '--text', 'example-app Open'])
  })
})

describe('DecisionCard: plain flag', () => {
  it('shows the ask and note, and approves', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    render(<DecisionCard needs={plainFlag} api={api} />)
    expect(screen.getByRole('heading', { name: 'Verify: dmg opens on your Mac' })).toBeInTheDocument()
    expect(screen.getByText(/Download the arm64 dmg/)).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    await screen.findByRole('heading', { name: 'Approved' })
    expect(api.calls[0]).toEqual([...base('cmdr-31'), '--action', 'approve'])
  })
})

describe('DecisionCard: answered elsewhere', () => {
  it('only resumes a saved answer when a card has left the snapshot', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    const onDone = vi.fn()
    render(<DecisionCard needs={optionsCard} api={api} onDone={onDone} />)
    api.items = api.items.filter(i => i !== optionsCard) // answered in the TUI meanwhile
    await user.keyboard('1{Enter}')
    await screen.findByRole('heading', { name: 'No longer needed' })
    expect(api.calls).toHaveLength(1)
    expect(api.calls[0]).toContain('--resume-only')
    expect(onDone).toHaveBeenCalledWith({ kind: 'gone' })
  })

  it('keeps a failed submit retryable after cleanup removes its snapshot card', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    api.next = { ok: false, exit: 1, stderr: 'queue busy' }
    const { rerender } = render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('1{Enter}')
    await screen.findByText('queue busy')
    api.items = []
    rerender(<DecisionCard needs={optionsCard} api={api} gone />)
    expect(screen.getByText('queue busy')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /submit/i }))
    await screen.findByRole('heading', { name: 'No longer needed' })
    expect(api.calls).toHaveLength(2)
    expect(api.calls[1]).toContain('--resume-only')
  })

  it('switches to "no longer needed" when the host says so', async () => {
    const api = fakeApi()
    const { rerender } = render(<DecisionCard needs={optionsCard} api={api} />)
    rerender(<DecisionCard needs={optionsCard} api={api} gone />)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'No longer needed' })).toHaveFocus())
    expect(screen.queryByRole('radio')).not.toBeInTheDocument()
  })

  it('keeps the confirmation after its own submit removes the flag', async () => {
    const user = userEvent.setup()
    const api = fakeApi()
    const { rerender } = render(<DecisionCard needs={optionsCard} api={api} />)
    await user.keyboard('1{Enter}')
    await screen.findByRole('heading', { name: 'Answer sent' })
    rerender(<DecisionCard needs={optionsCard} api={api} gone />)
    expect(screen.getByRole('heading', { name: 'Answer sent' })).toBeInTheDocument()
  })
})
