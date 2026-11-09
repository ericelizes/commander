// Settings > Agents > Usage: every account, how much of its plan is left, who
// is spending it, and connecting or disconnecting it right here.

import { ChevronDown, Plus } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { chatBackend, refreshChats, type ChatAccount, type SigninStart } from '../chatApi'
import { clock, pct, providerKey, usageState, type AgentRow } from '../providers'
import { getAgentUsage, type AccountTokens } from '../settingsApi'
import { useStore } from '../store'
import { roleLabel } from '../viewmodel'
import './usage.css'

export function UsageTab({ rows }: { rows: AgentRow[] }) {
  const allAccounts = useStore((s) => s.accounts)
  const nowSec = useStore((s) => s.nowSec)
  const config = useStore((s) => s.config)
  const [tokens, setTokens] = useState<Record<string, AccountTokens>>({})
  // Profiles signed out, added or picked for sign-in here stay listed until the view closes.
  const [kept, setKept] = useState<string[]>([])
  const keep = (id: string) => setKept((k) => (k.includes(id) ? k : [...k, id]))
  useEffect(() => {
    if (!config) return
    const load = () => void getAgentUsage(config).then((list) => setTokens(Object.fromEntries(list.map((a) => [a.key, a]))), () => {})
    load()
    const t = setInterval(load, 60_000)
    return () => clearInterval(t)
  }, [config])
  const counts = useMemo(() => {
    const m: Record<string, number> = {}
    for (const r of rows) m[providerKey(r.provider)] = (m[providerKey(r.provider)] ?? 0) + 1
    return m
  }, [rows])
  const keyOf = (a: ChatAccount) => (a.runtime === 'codex' ? 'codex' : `claude:${a.name}`)
  // Connected accounts, plus a signed-out profile people still need: one with
  // a known identity, agents bound to it, or picked here. Never empty slots.
  const accounts = allAccounts.filter((a) => a.logged_in || kept.includes(accountId(a)) || (counts[keyOf(a)] ?? 0) > 0 || !!(a.email || a.account_uuid))
  return (
    <div role="tabpanel" aria-label="Usage" className="accts">
      <h3>Accounts</h3>
      {accounts.map((a) => <UsageCard key={accountId(a)} a={a} agents={counts[keyOf(a)] ?? 0} tokens={tokens[keyOf(a)]} nowSec={nowSec} onSignedOut={() => keep(accountId(a))} />)}
      {accounts.length === 0 && <p className="settings-sub">No connected accounts.</p>}
      <AddAccount listed={accounts} keep={keep} />
    </div>
  )
}

const accountId = (a: ChatAccount) => `${a.runtime}:${a.name}`

// A Claude profile named codex would reach Codex's sign-in routes, which are
// keyed by name: the app shows it and never signs it in or out.
const reservedName = (a: ChatAccount) => a.runtime !== 'codex' && a.name === 'codex'

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

// An old daemon has no sign-in routes until the next install.
const friendly = (error?: string) => (error === 'HTTP 404' ? 'Connecting from here needs the latest Commander install.' : error || 'That didn\'t work. Try again.')

type Flow = 'idle' | 'starting' | 'open' | 'sending' | 'confirm-disconnect' | 'disconnecting'

export function UsageCard({ a, agents, tokens, nowSec, onSignedOut }: { a: ChatAccount; agents: number; tokens?: AccountTokens; nowSec: number; onSignedOut?: () => void }) {
  const config = useStore((s) => s.config)
  const backend = config ? chatBackend(config) : null
  const [flow, setFlow] = useState<Flow>('idle')
  const [start, setStart] = useState<SigninStart | null>(null)
  const [code, setCode] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const isCodex = a.runtime === 'codex'
  const provider = isCodex ? 'Codex' : 'Claude'
  const known = !!(a.email || a.account_uuid)
  const title = a.email || a.account_uuid || (a.logged_in ? 'Identity unavailable' : 'Identity unknown')
  const usage = a.logged_in && !a.duplicate_of ? usageState(a.usage, nowSec) : 'unknown'

  const finish = async (message: string) => {
    setFlow('idle'); setStart(null); setCode(''); setNote(message)
    await refreshChats()
  }
  const begin = async () => {
    if (!backend?.startSignin) return
    setError(''); setNote(''); setFlow('starting')
    const r = await backend.startSignin(a.name).catch(() => ({ ok: false, error: '' }) as SigninStart)
    if (!r.ok) { setFlow('idle'); setError(friendly(r.error)); return }
    setStart(r); setFlow('open')
  }
  const cancel = () => {
    void backend?.cancelSignin?.(a.name)
    setFlow('idle'); setStart(null); setCode(''); setError('')
  }
  const sendCode = async (e: FormEvent) => {
    e.preventDefault()
    if (!backend?.sendSigninCode || !code.trim()) return
    setError(''); setFlow('sending')
    const r: { ok: boolean; error?: string; email?: string } = await backend.sendSigninCode(a.name, code.trim()).catch(() => ({ ok: false, error: '' }))
    setCode('')
    if (!r.ok) { setFlow('idle'); setStart(null); setError(friendly(r.error)); return }
    await finish(r.email ? (a.logged_in && r.email === a.email ? `Still connected as ${r.email}. That sign-in used the same account.` : `Connected as ${r.email}.`) : 'Connected.')
  }
  const disconnect = async () => {
    if (!backend?.signOut) return
    setError(''); setFlow('disconnecting')
    const r = await backend.signOut(a.name).catch(() => ({ ok: false, error: '' }))
    if (!r.ok) { setFlow('idle'); setError(friendly(r.error)); return }
    onSignedOut?.()
    await finish(`Disconnected ${provider}.`)
  }

  // Codex finishes on the provider's page, with nothing to paste back here.
  useEffect(() => {
    if (flow !== 'open' || !isCodex || !backend?.signinState) return
    const t = setInterval(() => {
      void backend.signinState!(a.name).then((state) => {
        if (state === 'done') void finish('Connected.')
        if (state === 'failed' || state === 'none') { setFlow('idle'); setStart(null); setError('The sign-in ended before it finished. Start again.') }
      }, () => {})
    }, 2000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flow, isCodex, a.name])

  const busy = flow !== 'idle'
  const sub = `${provider}, ${a.logged_in ? plural(agents, 'agent') : 'not connected'}`
  return (
    <section className={`acct${a.logged_in ? '' : ' is-off'}`} aria-label={`${provider}: ${title}`}>
      <header className="acct-head">
        <span className="acct-mark" data-runtime={a.runtime} aria-hidden>{isCodex ? 'Cx' : 'C'}</span>
        <div className="acct-who">
          <b>{title}</b>
          <span>
            {sub}
            {usage === 'out' && <em className="is-out">, {a.usage?.out_until ? `out until ${clock(a.usage.out_until, nowSec)}` : 'out of usage'}</em>}
            {usage === 'warning' && <em className="is-warning">, running low</em>}
          </span>
        </div>
        {!busy && !reservedName(a) && (
          <div className="acct-actions">
            {a.logged_in ? (
              <>
                <button type="button" className="acct-btn" onClick={() => void begin()}>{a.duplicate_of ? 'Switch account' : 'Reconnect'}</button>
                <button type="button" className="acct-btn" onClick={() => { setNote(''); setError(''); setFlow('confirm-disconnect') }}>Disconnect</button>
              </>
            ) : (
              <button type="button" className="btn btn-primary" onClick={() => void begin()}>{known || agents > 0 ? 'Reconnect' : 'Connect'}</button>
            )}
          </div>
        )}
      </header>

      {reservedName(a) && <p className="acct-warn" role="status">This Claude profile is named codex, which Codex uses, so it can't be signed in or out here. Rename its folder under claude-accounts to manage it.</p>}
      {!a.logged_in && agents > 0 && !reservedName(a) && <p className="acct-warn" role="status">{plural(agents, 'agent')} on this profile can't run until you sign it in again.</p>}

      {a.logged_in && a.duplicate_of && <p className="acct-warn" role="status">Same identity as another connected Claude profile, so it adds no usage. Switch it to a different Claude account.</p>}

      {a.logged_in && !a.duplicate_of && (
        <>
          <div className="acct-meters">
            <Meter name="5 hours" w={a.usage?.five_hour} nowSec={nowSec} />
            <Meter name="7 days" w={a.usage?.seven_day} nowSec={nowSec} />
          </div>
          {tokens && <TopAgents t={tokens} />}
        </>
      )}

      {flow === 'starting' && <p className="acct-quiet" role="status">Starting the sign-in…</p>}
      {(flow === 'open' || flow === 'sending') && start && (
        <div className="acct-flow">
          <ol>
            <li>
              <span>{isCodex ? 'Open the sign-in page.' : 'Sign in with the account you want here.'} A private window helps if your browser is signed in to a different one.</span>
              <a className="btn btn-primary" href={start.url} target="_blank" rel="noreferrer noopener">Open sign-in page</a>
            </li>
            {isCodex ? (
              <li>
                <span>Enter this code on that page. This finishes by itself once you do.</span>
                <code className="acct-code">{start.user_code}</code>
              </li>
            ) : (
              <li>
                <span>Paste the code the page gives you.</span>
                <form className="acct-code-form" onSubmit={(e) => void sendCode(e)}>
                  <input aria-label={`Sign-in code for ${provider}`} placeholder="Sign-in code" autoComplete="off" spellCheck={false} value={code} disabled={flow === 'sending'} onChange={(e) => setCode(e.target.value)} />
                  <button type="submit" className="btn btn-primary" disabled={!code.trim() || flow === 'sending'}>{flow === 'sending' ? 'Connecting…' : 'Connect'}</button>
                </form>
              </li>
            )}
          </ol>
          <button type="button" className="acct-btn" disabled={flow === 'sending'} onClick={cancel}>Cancel</button>
        </div>
      )}

      {(flow === 'confirm-disconnect' || flow === 'disconnecting') && (
        <div className="acct-confirm" role="alertdialog" aria-label={`Disconnect ${provider}`}>
          <p>{agents > 0 ? `${plural(agents, 'agent')} on this account will stop working until you connect it again.` : 'Nothing is running on this account right now.'}</p>
          <div>
            <button type="button" className="btn acct-danger" disabled={flow === 'disconnecting'} onClick={() => void disconnect()}>{flow === 'disconnecting' ? 'Disconnecting…' : 'Disconnect'}</button>
            <button type="button" className="acct-btn" disabled={flow === 'disconnecting'} onClick={() => setFlow('idle')}>Keep connected</button>
          </div>
        </div>
      )}

      {note && <p className="acct-quiet" role="status">{note}</p>}
      {error && <p className="state-error" role="alert">{error}</p>}
    </section>
  )
}

function Meter({ name, w, nowSec }: { name: string; w?: { utilization: number; resets_at?: number }; nowSec: number }) {
  const p = pct(w, nowSec)
  const tone = p === undefined ? '' : p >= 100 ? ' is-out' : p >= 80 ? ' is-warning' : ''
  return (
    <div className={`acct-meter${tone}`}>
      <div className="acct-meter-top">
        <span>{name}</span>
        <b>{p === undefined ? 'No data' : `${p}%`}</b>
      </div>
      <span className="acct-bar" role="meter" aria-label={name} aria-valuemin={0} aria-valuemax={100} aria-valuenow={p ?? 0}>
        <span style={{ width: `${Math.min(100, p ?? 0)}%` }} />
      </span>
      <small>{w?.resets_at && nowSec < w.resets_at ? `Resets ${clock(w.resets_at, nowSec)}` : ' '}</small>
    </div>
  )
}

// modelName reads claude-opus-5-5 as Opus 5.5; other providers' names stay as they are.
export function modelName(model: string): string {
  const m = /^claude-([a-z]+)-(\d+)-(\d+)/.exec(model)
  return m ? `${m[1].charAt(0).toUpperCase()}${m[1].slice(1)} ${m[2]}.${m[3]}` : model || 'Unknown model'
}

// TopAgents is who used an account's tokens: today and this week up front, the
// agents that used the most behind a disclosure.
function TopAgents({ t }: { t: AccountTokens }) {
  const snapshot = useStore((s) => s.snapshot)
  return (
    <details className="acct-tokens" aria-label="Top agents">
      <summary>
        <span><b>{fmtTokens(t.today_tokens)}</b> tokens today, <b>{fmtTokens(t.week_tokens)}</b> this week</span>
        <ChevronDown size={14} aria-hidden />
      </summary>
      {t.agents.map((g) => (
        <div key={`${g.role}:${g.model}`} className="acct-agent" title={`${g.role}, ${g.week_turns} turns this week${g.week_usd ? `, $${g.week_usd.toFixed(2)}` : ''}`}>
          <span className="acct-agent-name">{roleLabel(snapshot, g.role)}</span>
          <span className="acct-agent-model">{modelName(g.model)}</span>
          <span className="acct-agent-n">{fmtTokens(g.week_tokens)}</span>
        </div>
      ))}
      {t.agents.length === 0 && <div className="acct-agent"><span className="acct-agent-model">No turns recorded this week.</span></div>}
    </details>
  )
}

/** fmtTokens is a token count at a glance: 950, 12k, 3.4M, 3.1B. */
export function fmtTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`
  if (n < 1_000_000_000) return `${(n / 1_000_000).toFixed(n < 10_000_000 ? 1 : 0)}M`
  return `${(n / 1_000_000_000).toFixed(n < 10_000_000_000 ? 1 : 0)}B`
}

function AddAccount({ listed, keep }: { listed: ChatAccount[]; keep: (id: string) => void }) {
  const accounts = useStore((s) => s.accounts)
  const config = useStore((s) => s.config)
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => { if (open) input.current?.focus() }, [open])
  const add = async () => {
    const want = name.trim()
    if (!want) return
    setError('')
    // An existing profile is signed in again, never created a second time.
    const existing = accounts.find((a) => a.runtime === 'claude' && a.name === want)
    if (existing) {
      keep(accountId(existing))
      setNote(existing.logged_in ? 'That profile is already connected.' : 'That profile already exists. Press Reconnect on it to sign in.')
      setName(''); setOpen(false)
      return
    }
    if (want === 'codex') { setError('codex is the Codex account. Use Connect Codex, or pick another name.'); return }
    const b = config && chatBackend(config)
    if (!b?.addAccount) return
    const r = await b.addAccount(want)
    if (!r.ok) { setError(r.error ?? 'Could not add the account.'); return }
    keep(`claude:${r.account ?? want}`); setNote('Account added. Press Connect on it to sign in.')
    setName(''); setOpen(false)
    await refreshChats()
  }
  const codex = accounts.find((a) => a.runtime === 'codex')
  return (
    <div className="acct-add">
      {codex && !listed.some((a) => a.runtime === 'codex') && <button type="button" className="acct-btn" onClick={() => { setNote(''); keep(accountId(codex)) }}>Connect Codex</button>}
      {open ? (
        <form onSubmit={(e) => { e.preventDefault(); void add() }}>
          <input ref={input} value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="Name it, like second" aria-label="Account name" />
          <button type="submit" className="btn btn-primary" disabled={!name.trim()}>Add account</button>
          <button type="button" className="acct-btn" onClick={() => { setOpen(false); setError('') }}>Cancel</button>
        </form>
      ) : (
        <button type="button" className="acct-btn acct-add-open" onClick={() => { setNote(''); setOpen(true) }}><Plus size={14} aria-hidden />Add or reconnect a Claude account</button>
      )}
      {note && <p className="acct-quiet" role="status">{note}</p>}
      {error && <p className="state-error" role="alert">{error}</p>}
    </div>
  )
}
