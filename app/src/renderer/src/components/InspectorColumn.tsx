// The focus pane, beside the sidebar: what is selected (a workstream, a track,
// an ask's workstream, or Commander) named at the top, and its tabs under it.
// A selected NEEDS YOU ask adds a tab for its pitch beside them; it never
// replaces them.

import { Activity, Lightbulb, Workflow as WorkflowIcon } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { NeedsYouItem } from '../../../features/decisions'
import { InspectorPane, openInPanel, type InspectorWindow, type PinnedTab } from '../../../features/inspector'
import type { TabInfo } from '../../../features/inspector/types'
import { useApis } from '../api'
import { demoBridge, demoClient, demoMode } from '../demo'
import { useStore } from '../store'
import { useAppDown } from './StartScreen'
import { focusOf, PitchView, SavedPitchView } from './PitchView'
import { DerivedDiagram, diagramFor } from './DerivedDiagram'
import { Overview, OverviewBoundary, WorkstreamHeader } from './Overview'
import type { FocusTarget } from './FocusHeader'
import type { SnapWorkstream } from '../../../shared/snapshot'
import { askKey, findTrack, trackTitle, wsKey } from '../viewmodel'
import { setWeb } from '../pageCapture'
import { setPanel } from '../screen'
import { useChatTarget } from './ChatSlot'

export function InspectorColumn() {
  const snapshot = useStore((s) => s.snapshot)
  const selected = useStore((s) => s.selected)
  const apis = useApis()

  // The ask (if one is selected) and the cockpit window whose tabs to show.
  const { ask, win, label } = useMemo(() => {
    const out: { ask: NeedsYouItem | null; win: InspectorWindow; label: string; askWs: SnapWorkstream | null } = { ask: null, win: {}, label: 'Commander', askWs: null }
    if (!snapshot) return out
    const byKey = (key: string) => snapshot.workstreams.find((w) => wsKey(w) === key)
    if (selected.kind === 'ws') {
      const w = byKey(selected.key)
      if (w) Object.assign(out, { win: { ws: w.bead_id, project: w.project }, label: w.title })
    } else if (selected.kind === 'agent') {
      const w = byKey(selected.ws)
      if (w) Object.assign(out, { win: { ws: w.bead_id, project: w.project }, label: w.title })
    } else if (selected.kind === 'ask') {
      const a = snapshot.needs_you.find((n) => askKey(n) === selected.key) ?? null
      out.ask = a
      const w = a && snapshot.workstreams.find((x) => x.project === a.project && x.title === a.workstream)
      if (w) Object.assign(out, { win: { ws: w.bead_id, project: w.project }, label: w.title, askWs: w })
    }
    return out
  }, [snapshot, selected])

  // The selected ask keeps its card even after it leaves the snapshot, so the
  // card can say "answered" / "no longer needed" instead of vanishing.
  const [held, setHeld] = useState<NeedsYouItem | null>(null)
  const selKey = selected.kind === 'ask' ? selected.key : ''
  useEffect(() => {
    if (ask) setHeld(ask)
    else if (!selKey) setHeld(null)
  }, [ask, selKey])
  const card = selKey && held && askKey(held) === selKey ? held : null

  // The pitch tab shows when an ask is opened; null while a list tab (a page, a PR) is selected.
  const [activePinned, setActivePinned] = useState<string | null>(null)
  const selectedAt = useStore((s) => s.selectedAt)
  useEffect(() => {
    if (!selKey) return
    // A decision reads on its workstream's Overview; its choices sit above the chat's input.
    setActivePinned('overview')
    useStore.getState().setChatTab(false)
  }, [selKey, selectedAt])

  // A focused workstream opens on Overview; Workflow remains a separate tab for that
  // track's (absent from an older cmdrd: no tab).
  const workflowOpen = useStore((s) => s.workflowOpen)
  const flow = useMemo(() => {
    const cardWs = card && snapshot?.workstreams.find((w) => w.project === card.project && w.title === card.workstream)
    const key = selected.kind === 'ws' ? selected.key : cardWs ? wsKey(cardWs) : ''
    if (!key) return null
    const row = snapshot?.display?.workstreams.find((r) => r.key === key)
    const track = (card ? card.track_id || undefined : workflowOpen?.ws === key ? workflowOpen.track : undefined) ?? row?.scope
    const t = findTrack(row, track)
    // The diagram cmdrd derived from beads (absent from an older cmdrd: the list alone).
    // A track clicked in it may have no sidebar row (one open track): it reads by its own title.
    const dg = diagramFor(row, track)
    const on = t ?? (track && track !== row?.scope ? undefined : row)
    const target: FocusTarget | null = row
      ? { n: row.n, title: row.title, letter: t?.letter, track: track ? trackTitle(t?.title ?? dg?.title ?? '') : undefined, kind: on?.kind, text: on?.text, progress: on?.progress }
      : null
    const ws = snapshot?.workstreams.find((w) => wsKey(w) === key)
    return row && target && ws ? { dg, row: row.key, boardRow: row, ws, track, target, key: `${row.key}/${track ?? ''}` } : null
  }, [snapshot, selected, workflowOpen, card])
  const focusedWs = selected.kind === 'ws' ? selected.key : ''
  useEffect(() => {
    if (focusedWs && !selKey) setActivePinned('overview')
    // A new focus opens on Overview; snapshots alone do not move the tab.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusedWs])
  useEffect(() => {
    if (workflowOpen && selected.kind === 'ws' && workflowOpen.ws === selected.key) setActivePinned(workflowOpen.tab ?? 'overview')
    // Only a new click brings the tab forward; snapshots and the selection alone do not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workflowOpen?.at])
  // A link to a page: open it here once its workstream's panel is the one showing,
  // so the tab lands in that workstream rather than the one the link was clicked from.
  const pendingPage = useStore((s) => s.pendingPage)
  useEffect(() => {
    if (!pendingPage || flow?.row !== pendingPage.ws) return
    openInPanel(pendingPage.url, pendingPage.title)
    setActivePinned(null)
    useStore.getState().clearPendingPage()
  }, [pendingPage, flow?.row])

  // What the app is focused on: the open workflow (a track's, when one is open),
  // else the selection's workstream; null with nothing focused.
  const focus = useMemo<FocusTarget | null>(() => {
    const rows = snapshot?.display?.workstreams
    if (card) {
      const ws = snapshot?.workstreams.find((w) => w.project === card.project && w.title === card.workstream)
      const row = rows?.find((r) => (ws ? r.key === wsKey(ws) : r.project === card.project && r.title === card.workstream))
      return focusOf(card, row, card.track_letter ?? '')
    }
    if (flow) return flow.target
    const key = selected.kind === 'ws' ? selected.key : selected.kind === 'agent' ? selected.ws : ''
    const row = key ? rows?.find((r) => r.key === key) : undefined
    if (row) return { n: row.n, title: row.title, kind: row.kind, text: row.text, progress: row.progress }
    return null
  }, [flow, card, snapshot, selected])
  const setFocus = useStore((s) => s.setFocus)
  useEffect(() => setFocus(focus), [focus, setFocus])

  const down = useAppDown()

  const modalOpen = useModalOpen()

  // A narrow window: the chat is the first tab. It gets a dot when its agent
  // writes while another tab shows.
  const narrow = useStore((s) => s.narrow)
  const chatTab = useStore((s) => s.chatTab)
  const chatRole = useChatTarget().role
  const chatSeq = useStore((s) => s.chats[chatRole]?.last_seq ?? 0)
  const [seenSeq, setSeenSeq] = useState(chatSeq)
  useEffect(() => {
    if (!narrow || chatTab) setSeenSeq(chatSeq)
  }, [narrow, chatTab, chatSeq])

  const pinned = useMemo<PinnedTab[]>(() => {
    const tabs: PinnedTab[] = []
    if (narrow) tabs.push({ id: 'chat', title: 'Chat', content: null, dot: !chatTab && chatSeq > seenSeq })
    if (flow) {
      tabs.push({
        id: 'overview',
        title: 'Current status',
        icon: <Activity size={13} aria-hidden />,
        content: <OverviewBoundary key={`o${flow.key}`}><Overview part="status" ws={flow.ws} row={flow.boardRow} track={flow.track} onWorkflow={() => setActivePinned('workflow')} onTrack={(id) => useStore.getState().openWorkflow(flow.row, id)} /></OverviewBoundary>,
      })
      tabs.push({
        id: 'pitch',
        title: 'Pitch',
        icon: <Lightbulb size={13} aria-hidden />,
        content: <OverviewBoundary key={`p${flow.key}`}><Overview part="pitch" ws={flow.ws} row={flow.boardRow} track={flow.track} onWorkflow={() => setActivePinned('workflow')} onTrack={(id) => useStore.getState().openWorkflow(flow.row, id)} /></OverviewBoundary>,
      })
      tabs.push({
        id: 'workflow',
        title: 'Workflow',
        icon: <WorkflowIcon size={13} aria-hidden />,
        // No close: the workflow is what focus is for.
        content: (
          <div className="pitch-pane">
            <div className="decision-scroll">
              <article className="fx">
                {flow.dg ? (
                  <DerivedDiagram key={`d${flow.key}`} diagram={flow.dg} onOpenTrack={(id) => useStore.getState().openWorkflow(flow.row, id)} />
                ) : (
                  <p className="dg-empty">Nothing planned yet</p>
                )}
              </article>
            </div>
          </div>
        ),
      })
    }
    return tabs
  }, [flow, narrow, chatTab, chatSeq, seenSeq])
  // A decision tab an agent opened stays only while that decision waits on you,
  // named by its question rather than its bead id. The open decision's own tab
  // already shows it.
  const asks = snapshot?.needs_you
  const openBead = card?.bead_id
  const shapeTabs = useCallback((tabs: TabInfo[]) => tabs.flatMap((t) => {
    if (t.kind !== 'decision') return [t]
    const waiting = asks?.find((n) => n.bead_id === t.target)
    return waiting && t.target !== openBead ? [{ ...t, title: waiting.title }] : []
  }), [asks, openBead])
  // A decision tab an agent opened (cmdr open <bead>) draws the same card.
  const renderDecision = (tab: { target: string }) => {
    const item = snapshot?.needs_you?.find((n) => n.bead_id === tab.target)
    if (!apis) return null
    if (!item) return <SavedPitchView project={win?.project || tab.target.replace(/-[^-]+$/, '')} bead={tab.target} />
    return (
      <PitchView key={item.bead_id} item={item} />
    )
  }
  const shownPinned = narrow && chatTab ? 'chat' : ((activePinned === 'workflow' || activePinned === 'overview' || activePinned === 'pitch') && !flow) ? null : activePinned
  const onPinned = (id: string | null) => {
    useStore.getState().setChatTab(id === 'chat')
    if (id !== 'chat') setActivePinned(id)
  }

  const demo = demoMode()
  const client = useMemo(() => (demo ? demoClient(demo) : undefined), [demo])
  const bridge = useMemo(() => (demo === 'web' ? null : demo === 'electron' ? demoBridge() : undefined), [demo])

  return (
    <div className="insp-host" aria-label={label}>
      {/* Until the board says which workstream this is, there is no window to list:
          asking early would flash Commander's own tabs. */}
      {apis && (win.ws || selected.kind === 'commander') && (
        <InspectorPane
          header={flow ? <WorkstreamHeader ws={flow.ws} row={flow.boardRow} track={flow.track} /> : undefined}
          api={apis.insp}
          window={win}
          client={client}
          bridge={bridge}
          pinned={pinned}
          activePinned={shownPinned}
          onPinned={onPinned}
          covered={down || modalOpen}
          onTabs={(tabs, showing, web) => {
            setPanel(tabs, showing)
            setWeb(web)
          }}
          renderDecision={renderDecision}
          shapeTabs={shapeTabs}
        />
      )}
    </div>
  )
}

// useModalOpen is true while any modal dialog is on screen. Web pages are native
// views drawn above the whole page, so they must hide for a dialog to show.
function useModalOpen() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const check = () => setOpen(!!document.querySelector('[aria-modal="true"]'))
    const mo = new MutationObserver(check)
    mo.observe(document.body, { childList: true, subtree: true })
    check()
    return () => mo.disconnect()
  }, [])
  return open
}
