import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { modelName, UsageCard, UsageTab } from '../src/renderer/src/components/UsageTab'
import { useStore } from '../src/renderer/src/store'
import type { ChatAccount } from '../src/renderer/src/chatApi'

afterEach(cleanup)
const startSignin = vi.fn()
const sendSigninCode = vi.fn()
const cancelSignin = vi.fn()
const signinState = vi.fn()
const signOut = vi.fn()
const addAccount = vi.fn()
vi.mock('../src/renderer/src/chatApi', async (original) => ({ ...(await original<object>()), chatBackend: () => ({ startSignin, sendSigninCode, cancelSignin, signinState, signOut, addAccount }), refreshChats: vi.fn() }))
beforeEach(() => {
  for (const f of [startSignin, sendSigninCode, cancelSignin, signinState, signOut, addAccount]) f.mockReset()
  useStore.setState({ config: { apiUrl: 'http://fixture.test', token: 'fixture' } as never })
})
const account: ChatAccount = { name: 'default', runtime: 'claude', logged_in: true, email: 'one@example.test', account_uuid: 'one' }

it('connects from the app: a link, a pasted code, then who it connected', async () => {
  startSignin.mockResolvedValue({ ok: true, url: 'https://claude.example/signin' })
  sendSigninCode.mockResolvedValue({ ok: true, email: 'two@example.test' })
  render(<UsageCard a={{ ...account, logged_in: false, email: undefined, account_uuid: undefined }} agents={0} nowSec={1} />)
  expect(screen.getByText('Claude, not connected')).toBeInTheDocument()
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Connect' })) })
  expect(screen.getByRole('link', { name: 'Open sign-in page' })).toHaveAttribute('href', 'https://claude.example/signin')
  fireEvent.change(screen.getByLabelText('Sign-in code for Claude'), { target: { value: ' abc#123 ' } })
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Connect' })) })
  expect(sendSigninCode).toHaveBeenCalledWith('default', 'abc#123')
  expect(screen.getByRole('status')).toHaveTextContent('Connected as two@example.test.')
  expect(screen.queryByRole('link', { name: 'Open sign-in page' })).toBeNull()
})

it('says when a reconnect signed in to the same account again', async () => {
  startSignin.mockResolvedValue({ ok: true, url: 'https://claude.example/signin' })
  sendSigninCode.mockResolvedValue({ ok: true, email: 'one@example.test' })
  render(<UsageCard a={account} agents={1} nowSec={1} />)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Reconnect' })) })
  fireEvent.change(screen.getByLabelText('Sign-in code for Claude'), { target: { value: 'abc' } })
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Connect' })) })
  expect(screen.getByRole('status')).toHaveTextContent('Still connected as one@example.test')
})

it('shows why a code failed and lets the sign-in start again', async () => {
  startSignin.mockResolvedValue({ ok: true, url: 'https://claude.example/signin' })
  sendSigninCode.mockResolvedValue({ ok: false, error: "That code didn't work. Start again and paste the newest code." })
  render(<UsageCard a={{ ...account, logged_in: false }} agents={0} nowSec={1} />)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Reconnect' })) })
  fireEvent.change(screen.getByLabelText('Sign-in code for Claude'), { target: { value: 'bad' } })
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Connect' })) })
  expect(screen.getByRole('alert')).toHaveTextContent("That code didn't work")
  expect(screen.getByRole('button', { name: 'Reconnect' })).toBeEnabled()
})

it('cancels an open sign-in', async () => {
  startSignin.mockResolvedValue({ ok: true, url: 'https://claude.example/signin' })
  render(<UsageCard a={account} agents={0} nowSec={1} />)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Reconnect' })) })
  fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(cancelSignin).toHaveBeenCalledWith('default')
  expect(screen.getByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
})

it('explains an install is needed when the daemon has no sign-in yet', async () => {
  startSignin.mockResolvedValue({ ok: false, error: 'HTTP 404' })
  render(<UsageCard a={account} agents={0} nowSec={1} />)
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Reconnect' })) })
  expect(screen.getByRole('alert')).toHaveTextContent('needs the latest Commander install')
})

it('warns what stops before disconnecting, and only disconnects when confirmed', async () => {
  signOut.mockResolvedValue({ ok: true })
  render(<UsageCard a={account} agents={3} nowSec={1} />)
  fireEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
  expect(signOut).not.toHaveBeenCalled()
  expect(screen.getByRole('alertdialog')).toHaveTextContent('3 agents on this account will stop working')
  fireEvent.click(screen.getByRole('button', { name: 'Keep connected' }))
  expect(signOut).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole('button', { name: 'Disconnect' }))
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Disconnect' })) })
  expect(signOut).toHaveBeenCalledWith('default')
  expect(screen.getByRole('status')).toHaveTextContent('Disconnected Claude')
})

it('connects Codex with a device code and finishes by itself', async () => {
  vi.useFakeTimers()
  try {
    startSignin.mockResolvedValue({ ok: true, url: 'https://auth.example/device', user_code: 'ABCD-1234' })
    signinState.mockResolvedValue('done')
    render(<UsageCard a={{ name: 'codex', runtime: 'codex', logged_in: false }} agents={0} nowSec={1} />)
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Connect' })) })
    expect(screen.getByText('ABCD-1234')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Sign-in code')).toBeNull()
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(screen.getByRole('status')).toHaveTextContent('Connected.')
  } finally {
    vi.useRealTimers()
  }
})

it('warns duplicate profiles add no usage, hides their meters, and offers to switch', () => {
  render(<UsageCard a={{ ...account, name: 'second', duplicate_of: 'default' }} agents={0} nowSec={1} />)
  expect(screen.getByRole('status')).toHaveTextContent('Same identity as another connected Claude profile, so it adds no usage')
  expect(screen.queryAllByRole('meter')).toHaveLength(0)
  expect(screen.getByRole('button', { name: 'Switch account' })).toBeInTheDocument()
})

it('leads with who is signed in, and keeps the top agents behind a disclosure', () => {
  const tokens = { key: 'claude:default', today_tokens: 106_000_000, week_tokens: 6_200_000_000, agents: [{ account: 'default', role: 'commander', model: 'claude-opus-5-5', today_tokens: 1, week_tokens: 1_900_000_000, today_turns: 1, week_turns: 2, week_usd: 0 }] } as never
  useStore.setState({ accounts: [account] })
  render(<UsageTab rows={[]} />)
  expect(screen.getByText('one@example.test').tagName).toBe('B')
  render(<UsageCard a={account} agents={1} nowSec={1} tokens={tokens} />)
  expect(screen.getByText('6.2B')).toBeInTheDocument()
  expect(screen.getByText('Opus 5.5').closest('details')).not.toHaveAttribute('open')
  useStore.setState({ accounts: [] })
})

it('names models people say, leaving others as they are', async () => {
  expect(modelName('claude-sonnet-5-5')).toBe('Sonnet 5.5')
  expect(modelName('gpt-6.1-sol')).toBe('gpt-6.1-sol')
  await waitFor(() => expect(modelName('')).toBe('Unknown model'))
})

it('lists connected identities with provider labels, never empty slots, and guards a Claude profile named codex', () => {
  useStore.setState({ accounts: [account,
    { name: 'spare', runtime: 'claude', logged_in: false },
    { name: 'codex', runtime: 'claude', logged_in: true, email: 'two@example.test' },
    { name: 'codex', runtime: 'codex', logged_in: true, email: 'one@example.test' }] })
  render(<UsageTab rows={[]} />)
  expect(screen.getByRole('heading', { name: 'Accounts' })).toBeInTheDocument()
  expect(screen.getAllByRole('region').map((r) => r.getAttribute('aria-label'))).toEqual([
    'Claude: one@example.test', 'Claude: two@example.test', 'Codex: one@example.test'])
  expect(screen.queryByText(/not connected/)).toBeNull()
  expect(screen.queryByText('Claude default')).toBeNull()
  // The codex-named Claude profile would sign Codex in or out: no actions on it.
  const reserved = screen.getByRole('region', { name: 'Claude: two@example.test' })
  expect(reserved).toHaveTextContent('named codex')
  expect(within(reserved).queryByRole('button')).toBeNull()
  useStore.setState({ accounts: [] })
})


it('keeps a disconnected profile listed with its note, then reconnects it', async () => {
  signOut.mockResolvedValue({ ok: true })
  startSignin.mockResolvedValue({ ok: true, url: 'https://claude.example/signin' })
  sendSigninCode.mockResolvedValue({ ok: true, email: 'three@example.test' })
  const spare: ChatAccount = { name: 'spare', runtime: 'claude', logged_in: true, email: 'three@example.test' }
  useStore.setState({ accounts: [spare] })
  render(<UsageTab rows={[]} />)
  const card = () => screen.getByRole('region', { name: /^Claude: / })
  fireEvent.click(within(card()).getByRole('button', { name: 'Disconnect' }))
  await act(async () => { fireEvent.click(within(card()).getByRole('button', { name: 'Disconnect' })) })
  // cmdrd now reports it signed out and forgets who it was.
  act(() => useStore.setState({ accounts: [{ name: 'spare', runtime: 'claude', logged_in: false }] }))
  expect(signOut).toHaveBeenCalledWith('spare')
  expect(within(card()).getByRole('status')).toHaveTextContent('Disconnected Claude.')
  expect(card()).toHaveTextContent('Claude, not connected')
  await act(async () => { fireEvent.click(within(card()).getByRole('button', { name: 'Connect' })) })
  fireEvent.change(screen.getByLabelText('Sign-in code for Claude'), { target: { value: 'abc' } })
  await act(async () => { fireEvent.click(within(card()).getByRole('button', { name: 'Connect' })) })
  expect(sendSigninCode).toHaveBeenCalledWith('spare', 'abc')
  expect(within(card()).getByRole('status')).toHaveTextContent('Connected as three@example.test.')
  useStore.setState({ accounts: [] })
})

it('lists a signed-out profile agents are bound to, with a Reconnect', () => {
  useStore.setState({ accounts: [{ name: 'default', runtime: 'claude', logged_in: false }] })
  render(<UsageTab rows={[{ provider: { runtime: 'claude', account: 'default' } } as never, { provider: { runtime: 'claude', account: 'default' } } as never]} />)
  const card = screen.getByRole('region', { name: 'Claude: Identity unknown' })
  expect(card).toHaveTextContent("2 agents on this profile can't run until you sign it in again.")
  expect(within(card).getByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
  useStore.setState({ accounts: [] })
})

it('Add with an existing profile name lists it for sign-in and never creates it again', async () => {
  useStore.setState({ accounts: [{ name: 'spare', runtime: 'claude', logged_in: false }, { name: 'codex', runtime: 'codex', logged_in: true, email: 'one@example.test' }] })
  render(<UsageTab rows={[]} />)
  expect(screen.queryByRole('region', { name: /Identity unknown/ })).toBeNull()
  fireEvent.click(screen.getByRole('button', { name: 'Add or reconnect a Claude account' }))
  fireEvent.change(screen.getByRole('textbox', { name: 'Account name' }), { target: { value: 'spare' } })
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Add account' })) })
  expect(addAccount).not.toHaveBeenCalled()
  expect(screen.getByRole('region', { name: 'Claude: Identity unknown' })).toBeInTheDocument()
  expect(screen.getByText(/already exists/)).toBeInTheDocument()
  // codex is Codex's name: refused before any call.
  fireEvent.click(screen.getByRole('button', { name: 'Add or reconnect a Claude account' }))
  fireEvent.change(screen.getByRole('textbox', { name: 'Account name' }), { target: { value: 'codex' } })
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Add account' })) })
  expect(addAccount).not.toHaveBeenCalled()
  expect(screen.getByRole('alert')).toHaveTextContent('codex is the Codex account')
  useStore.setState({ accounts: [] })
})

it('Connect Codex lists the signed-out Codex account and its device-code flow runs from there', async () => {
  vi.useFakeTimers()
  try {
    startSignin.mockResolvedValue({ ok: true, url: 'https://auth.example/device', user_code: 'WXYZ-9876' })
    signinState.mockResolvedValue('done')
    useStore.setState({ accounts: [account, { name: 'codex', runtime: 'codex', logged_in: false }] })
    render(<UsageTab rows={[]} />)
    expect(screen.queryByRole('region', { name: /^Codex/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Connect Codex' }))
    const card = screen.getByRole('region', { name: 'Codex: Identity unknown' })
    expect(screen.queryByRole('button', { name: 'Connect Codex' })).toBeNull()
    await act(async () => { fireEvent.click(within(card).getByRole('button', { name: 'Connect' })) })
    expect(startSignin).toHaveBeenCalledWith('codex')
    expect(within(card).getByText('WXYZ-9876')).toBeInTheDocument()
    // Once connected cmdrd reports an account id only: that is its title.
    useStore.setState({ accounts: [account, { name: 'codex', runtime: 'codex', logged_in: true, account_uuid: 'acct-7' }] })
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    const done = screen.getByRole('region', { name: 'Codex: acct-7' })
    expect(within(done).getByRole('status')).toHaveTextContent('Connected.')
    expect(screen.queryByRole('button', { name: 'Connect Codex' })).toBeNull()
  } finally {
    vi.useRealTimers()
    useStore.setState({ accounts: [] })
  }
})
