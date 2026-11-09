// The middle column's body: the live native chat of the selected agent
// (Commander, the workstream's Manager, or one of its Operators), attached on
// selection and replayed from seq 0 so the hydrated transcript is the history.
// A Commander or Manager chat that is not running starts on its own when
// nothing else holds its session; one a terminal holds gets an explicit,
// confirmed Take over. Until cmdrd's chat list answers, a quiet loader shows.

import { AskDock } from './AskDock'
import { useEffect, useMemo, useRef, useState } from 'react'
import { ChatHostContext, ChatPane, type ChatHost, type MenuItem } from '../../../features/chat'
import { ChatGraph } from './ChatGraph'
import { ChatWatching } from './ChatWatching'
import { MailChip } from './MailChip'
import { hasConnections, openInPanel } from '../../../features/inspector'
import { typing } from '../mailHold'
import { captureHost } from '../pageCapture'
import { screenContext } from '../screen'
import { chatBackend, refreshChats, useChatPoll, type ChatStatus, type ChatVerb } from '../chatApi'
import { switchTargets, type SwitchTarget } from '../providers'
import { UsageLimitNotice } from './UsageLimitNotice'
import { useStore } from '../store'
import { linkLabel, managerOfRole, operatorLabel, roleCostNote, wsKey } from '../viewmodel'
import { ConfirmDialog } from './ConfirmDialog'

export interface ChatTarget {
  role: string
  title: string
  kind: 'commander' | 'manager' | 'operator'
}

/**
 * useChatTarget: whose chat the selection shows. A workstream or an ask keeps
 * Commander (the right column follows the selection); a Manager or Operator
 * chat shows only when chosen as an agent.
 */
export function useChatTarget(): ChatTarget {
  const snapshot = useStore((s) => s.snapshot)
  const selected = useStore((s) => s.selected)
  return useMemo(() => {
    const commander: ChatTarget = { role: 'commander', title: 'Commander', kind: 'commander' }
    if (!snapshot || selected.kind === 'commander') return commander
    const byKey = (key: string) => snapshot.workstreams.find((w) => wsKey(w) === key)
    if (selected.kind !== 'agent') return commander
    const w = byKey(selected.ws)
    if (w && selected.key === w.manager_role) return { role: w.manager_role, title: `${w.title} Manager`, kind: 'manager' }
    // A nested workstream with a Manager of its own.
    const nested = managerOfRole(snapshot, selected.key)
    if (nested?.step) return { role: selected.key, title: `${nested.title} Manager`, kind: 'manager' }
    return { role: selected.key, title: w ? `${operatorLabel(selected.key, w.bead_id)} · ${w.title}` : selected.key, kind: 'operator' }
  }, [snapshot, selected])
}

type Dialog = null | 'handover' | 'fresh'

// What every chat may do in the app: bead links open a workstream or track on
// a tab, and agents can draw graphs inline.
const chatHost: ChatHost = {
  openBead: (id, tab) => useStore.getState().openLink(id, tab),
  describeBead: (id, tab) => linkLabel(useStore.getState().snapshot, id, tab),
  openBeadPage: (id, url, title) => hasConnections() && useStore.getState().openLinkPage(id, url, title),
  // Web links open as a tab in the side panel (signed in there) when the app has one.
  openUrl: (url) => {
    if (!hasConnections()) return false
    openInPanel(url)
    return true
  },
  agentName: (role) => {
    if (role === 'user') return 'You'
    if (role === 'commander') return 'Commander'
    if (role === 'cmdrd') return 'Commander'
    const owner = managerOfRole(useStore.getState().snapshot, role)
    if (owner) return `${owner.title} Manager`
    if (role.startsWith('operator-')) return `Operator ${role.replace(/^operator-(op-)?/, '')}`
    return role
  },
  Graph: ChatGraph,
  screen: screenContext,
  capture: captureHost,
  Watching: ChatWatching,
  MailChip,
  // Above the input: an ask's choices.
  Dock: ({ role }) => <AskDock role={role} />,
  typing,
}

export function ChatSlot() {
  return (
    <ChatHostContext.Provider value={chatHost}>
      <ChatSlotInner />
    </ChatHostContext.Provider>
  )
}

function ChatSlotInner() {
  useChatPoll() // the one poller: the navigator's Operators and the header chip read it too
  const config = useStore((s) => s.config)
  const engine = useStore((s) => s.chatEngine)
  const target = useChatTarget()
  const status = useStore((s) => s.chats[target.role]) as ChatStatus | undefined
  const costNote = useStore((s) => roleCostNote(s.snapshot?.fleet, target.role))
  const accounts = useStore((s) => s.accounts)
  const nowSec = useStore((s) => s.nowSec)
  const backend = useMemo(() => (config ? chatBackend(config) : null), [config])

  // Per-role UI state: who owns the session (from a 409), the last error, a call in flight.
  const [owned, setOwned] = useState<{ by: string; pid?: number } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState<ChatVerb | null>(null)
  const [dialog, setDialog] = useState<Dialog>(null)
  useEffect(() => {
    setOwned(null)
    setError('')
    setDialog(null)
  }, [target.role])
  useEffect(() => demoDialog(setDialog), [])

  // Auto-start: once per role per app session, so a failure shows instead of looping.
  const tried = useRef(new Set<string>())
  const ownedBy = owned?.by ?? status?.owned_by
  const canAutoStart = engine === 'on' && !!backend && target.kind !== 'operator' && !status?.running && !ownedBy && !busy
  useEffect(() => {
    if (!canAutoStart || tried.current.has(target.role)) return
    tried.current.add(target.role)
    void run('start')
  })

  if (engine !== 'on' || !backend) return <ChatLoading />

  // Who holds the session: cmdrd's status says so up front; a 409 on start says so late.
  const holder = status?.owned_by ? { by: status.owned_by, pid: status.owner_pid } : owned

  const run = async (verb: ChatVerb, body?: Record<string, unknown>) => {
    setDialog(null)
    setBusy(verb)
    setError('')
    try {
      const r = await backend.call(target.role, verb, body)
      if (r.ok) setOwned(null)
      else if (r.code === 409 && r.owned_by && r.owned_by !== 'engine') setOwned({ by: r.owned_by, pid: r.pid })
      else setError(r.error ?? `${verb} failed`)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      await refreshChats()
      setBusy(null)
    }
  }

  const dialogs = (
    <>
      {dialog === 'handover' && (
        <ConfirmDialog
          title={`Move ${target.title} to the app?`}
          confirm="Move to app"
          onConfirm={() => void run('handover')}
          onCancel={() => setDialog(null)}
        >
          <p>
            This closes it in the terminal{(owned?.pid ?? status?.owner_pid) ? ` (pid ${owned?.pid ?? status?.owner_pid})` : ''} and continues the <b>same session</b> here,
            with its full history.
          </p>
          <p className="muted">If the agent is mid-reply, wait for it to finish first.</p>
        </ConfirmDialog>
      )}
      {dialog === 'fresh' && (
        <ConfirmDialog
          title={`Start ${target.title} fresh?`}
          confirm="Start fresh"
          onConfirm={() => void run('fresh')}
          onCancel={() => setDialog(null)}
        >
          <p>
            It becomes a <b>new session</b> with today's instructions and an empty context. Its first step is reading this conversation, so it picks up
            what was in flight.
          </p>
          <p className="muted">If the agent is mid-reply, wait for it to finish first.</p>
        </ConfirmDialog>
      )}
    </>
  )

  if (status?.running) {
    // Switching restarts the chat on the other account or CLI and resumes the
    // same conversation (refused mid-turn; the refusal shows under the chat).
    const current = status.runtime === 'codex' ? { runtime: 'codex' as const } : { runtime: 'claude' as const, account: status.account }
    const targets = switchTargets(accounts, current, nowSec)
    const switchTo = (t: SwitchTarget, cont = false) => void run(t.verb, cont ? { ...t.body, continue: true } : t.body)
    const menu: MenuItem[] = targets.map((t) => ({ label: t.label, group: 'Switch to', checked: t.current, detail: t.detail, tone: t.state, onSelect: () => switchTo(t) }))
    if (target.kind !== 'operator') menu.push({ label: 'Start fresh', group: 'Session', detail: 'new context, reads this chat first', onSelect: () => setDialog('fresh') })
    const switching = busy === 'runtime' || busy === 'account' || busy === 'fresh'
    return (
      <>
        <ChatPane
          key={target.role}
          role={target.role}
          title={target.title}
          transport={backend.transport}
          costUsd={status.total_cost_usd}
          costNote={costNote}
          lastSeq={status.last_seq}
          menu={switching ? [] : menu}
        />
        {status.usage_limit && (
          <UsageLimitNotice limit={status.usage_limit} targets={targets.filter((t) => !t.current)} busy={switching} onSwitch={(t) => switchTo(t, true)} />
        )}
        {error && (
          <p className="state-error chat-slot-error" role="alert">
            {error}
          </p>
        )}
        {dialogs}
      </>
    )
  }

  if (holder) {
    // Live in a terminal (or a headless process): never Start (it would fork
    // the session). Its history replays here and the composer sends by mail;
    // a terminal-held one can Move to app (the confirmed handover).
    const headless = holder.by === 'headless'
    const pid = holder.pid ? ` (pid ${holder.pid})` : ''
    return (
      <>
        <ChatPane
          key={`${target.role}:terminal`}
          role={target.role}
          title={target.title}
          transport={backend.transport}
          costUsd={status?.total_cost_usd}
          costNote={costNote}
          lastSeq={status?.last_seq}
          terminal={{
            label: headless ? `headless process${pid}` : 'terminal session',
            title: headless
              ? `A headless Claude process${pid} holds this session; messages are delivered by mail.`
              : `Running in its tmux terminal${pid}; messages are delivered by mail. Move to app resumes the same session here.`,
            onMove: headless || busy ? undefined : () => setDialog('handover'),
          }}
        />
        {error && (
          <p className="state-error chat-slot-error" role="alert">
            {error}
          </p>
        )}
        {dialogs}
      </>
    )
  }

  const history = (status?.last_seq ?? 0) > 0
  // Starting (or about to): the loader, not a card.
  if (target.kind !== 'operator' && !ownedBy && !error) return <ChatLoading />
  const ownerPid = owned?.pid ?? status?.owner_pid
  return (
    <div className="chat-stack">
      {target.kind === 'operator' ? (
        <StateCard icon="terminal" title={`${target.title}`} compact={history}>
          <p>This Operator runs in the tmux cockpit, not in the app. Its chat shows here once it runs natively.</p>
        </StateCard>
      ) : (
        <StateCard icon={ownedBy ? 'terminal' : 'chat'} title={ownedBy ? 'Open in a terminal' : 'Chat didn’t start'} compact={history}>
          {ownedBy ? (
            <p>
              {target.title} is open in a {ownedBy === 'headless' ? 'background Claude process' : 'terminal'}
              {ownerPid ? ` (pid ${ownerPid})` : ''}. Move it here to chat in the app; the same session continues with its history.
            </p>
          ) : (
            <p>{target.title}&rsquo;s chat couldn&rsquo;t start.</p>
          )}
          <div className="state-actions">
            {ownedBy ? (
              <button type="button" className="btn btn-primary" disabled={!!busy} onClick={() => setDialog('handover')}>
                {busy === 'handover' ? 'Moving…' : 'Move to app'}
              </button>
            ) : (
              <button type="button" className="btn btn-primary" disabled={!!busy} onClick={() => void run('start')}>
                {busy === 'start' ? 'Starting…' : 'Try again'}
              </button>
            )}
          </div>
          {error && (
            <p className="state-error" role="alert">
              {error}
            </p>
          )}
        </StateCard>
      )}
      {history && (
        <div className="chat-stack-history">
          <ChatPane key={`${target.role}:ro`} role={target.role} title={target.title} transport={backend.transport} costUsd={status?.total_cost_usd} costNote={costNote} lastSeq={status?.last_seq} readonly="Not running" />
        </div>
      )}
      {dialogs}
    </div>
  )
}

// ?fixture=chat&dialog=handover opens that dialog for screenshots.
function demoDialog(set: (d: Dialog) => void): void {
  if (typeof location === 'undefined') return
  const q = new URLSearchParams(location.search)
  const d = q.get('dialog')
  if (q.get('fixture') === 'chat' && d === 'handover') set(d)
}

function StateCard({ icon, title, compact, children }: { icon: 'chat' | 'terminal'; title: string; compact?: boolean; children: React.ReactNode }) {
  return (
    <section className={`placeholder chat-placeholder${compact ? ' compact' : ''}`}>
      <div className="placeholder-card">
        <div className="placeholder-icon" aria-hidden>
          {icon === 'chat' ? (
            <svg width="28" height="28" viewBox="0 0 24 24" fill="none" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v8a2.5 2.5 0 0 1-2.5 2.5H10l-4.5 4v-4h0A2.5 2.5 0 0 1 4 13.5z" />
            </svg>
          ) : (
            <svg width="28" height="28" viewBox="0 0 24 24" fill="none" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">
              <rect x="3" y="4" width="18" height="16" rx="2.5" />
              <path d="M7 9l3 3-3 3M12.5 15H17" />
            </svg>
          )}
        </div>
        <h2>{title}</h2>
        {children}
      </div>
    </section>
  )
}

function ChatLoading() {
  return (
    <div className="chat-loading" aria-busy aria-label="Loading chat">
      <span />
      <span />
      <span />
      <span />
    </div>
  )
}
