import { PitchChoices, usePitch } from '../../renderer/src/components/PitchView'
import { trackTitle } from '../../renderer/src/viewmodel'
// The tree board where the app's sidebar and middle pane were: one header bar
// split at the tree's width and one grid of rows, the tree column first, drawn
// by board.ts from cmdrd's tree (GET /api/tree), so the tree and the board
// always line up. The popup holds the new details for what is selected, then the
// original focus pane's tabs (Workflow, a NEEDS YOU decision card, tabs an
// agent opened). What is open rides along with each message to the Commander.

import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { MyProgress, treeWorkstreams } from '../linear/MyProgress'
import { useLiveTree } from './liveTree'
import { askKey, wsKey } from '../../renderer/src/viewmodel'
import { host } from '../../renderer/src/host'
import { Splitter } from '../../renderer/src/components/Splitter'
import { useStore } from '../../renderer/src/store'
import { linearUrl } from '../linear/Ticket'
import { useIntegrations } from '../linear/settings'
import { mountBoard, type Board } from './board'
import { setTreeSelection, useTreeSelection } from './selection'
import type { Selection, Tree, TreeRow } from './types'
import './treeboard.css'

export async function fetchTree(apiUrl: string, token: string): Promise<Tree> {
  const res = await fetch(`${apiUrl}/api/tree`, { headers: { Authorization: `Bearer ${token}` } })
  if (!res.ok) throw new Error(`tree: HTTP ${res.status}`)
  return (await res.json()) as Tree
}

// focusFor points the original focus pane at what the tree has open: a step with a
// NEEDS YOU card opens that card; anything else opens its top-level workstream,
// on the nested workstream's Workflow when it is one.
function focusFor(sel: Selection | null, tree: Tree | null) {
  const st = useStore.getState()
  const snap = st.snapshot
  if (!sel || !snap) return st.goHome()
  const ask = sel.kind === 'step' ? snap.needs_you?.find((n) => n.bead_id === sel.id) : undefined
  if (ask) return st.select({ kind: 'ask', key: askKey(ask) })
  const rows = new Map((tree?.rows ?? []).map((r) => [r.id, r]))
  let row = rows.get(sel.kind === 'step' ? (tree?.steps.find((s) => s.id === sel.id)?.row ?? '') : sel.id)
  const nested = row
  while (row?.parent && rows.get(row.parent)) row = rows.get(row.parent)
  const top = row && snap.workstreams.find((w) => wsKey(w) === row!.key)
  if (!top) return st.goHome()
  st.select({ kind: 'ws', key: wsKey(top) })
  if (nested && nested.id !== top.bead_id) st.openWorkflow(wsKey(top), nested.id, 'workflow')
}

/** PanelAsk: the answer choices for the step open in the board's popup, when it is a card of Eric's. */
function PanelAsk() {
  const sel = useTreeSelection((s) => s.sel)
  const item = useStore((s) => (sel?.kind === 'step' ? s.snapshot?.needs_you?.find((n) => n.bead_id === sel.id) : undefined))
  return item ? <PanelAskCard key={item.bead_id} item={item} /> : null
}

function PanelAskCard({ item }: { item: NonNullable<ReturnType<typeof useStore.getState>['snapshot']>['needs_you'][number] }) {
  const { versions, answer } = usePitch(item)
  if (answer) return null
  const card = item.decision
  const question = card?.question && card.question !== 'Approve?' ? card.question : item.title
  const where = [item.workstream || '', item.track ? trackTitle(item.track) : ''].filter(Boolean).join(' › ')
  return (
    <div className="tb-panel-ask">
      <PitchChoices item={item} question={question} where={where} version={versions.length} />
    </div>
  )
}

/** TreeBoard: where the app's sidebar and middle pane were, one grid: the tree column (as wide as the sidebar, resized by the app's left splitter) and the board. */
export function TreeBoard({ treeWidth, onDragTree, onDragDone }: { treeWidth: number; onDragTree(dx: number): void; onDragDone(): void }) {
  const ref = useRef<HTMLDivElement>(null)
  const board = useRef<Board | null>(null)
  const tree = useRef<Tree | null>(null)
  const [error, setError] = useState('')
  const [ready, setReady] = useState(0)
  const cfg = useStore((s) => s.config)
  const version = useStore((s) => s.snapshot?.version ?? 0)
  // The Linear progress card, as the classic sidebar showed it, without the cycle list:
  // the whole cycle in the sidebar header, a top-level workstream's own tickets in its panel.
  const liveTree = useLiveTree((s) => s.tree)
  const progress = useMemo(() => treeWorkstreams(liveTree), [liveTree])
  const [slots, setSlots] = useState<{ linear: HTMLElement; inspector: HTMLElement } | null>(null)
  const [wsSlot, setWsSlot] = useState<{ el: HTMLElement; row: TreeRow } | null>(null)
  const openProgress = () => useStore.getState().select({ kind: 'progress' })

  useEffect(() => {
    if (!ref.current) return
    const b = mountBoard(ref.current, {
      onSelect: (s) => {
        setTreeSelection(s)
        focusFor(s, tree.current)
      },
      onChat: () => {},
      linearUrl: (id) => {
        const ws = useIntegrations.getState().linearWorkspace
        return ws ? linearUrl(id, ws) : null
      },
      openUrl: (url) => {
        if (host?.webview) host.webview.openExternal(url)
        else window.open(url, '_blank', 'noopener,noreferrer')
      },
      onPanel: (el, row) => setWsSlot(el && row && !row.parent ? { el, row } : null),
    })
    board.current = b
    setSlots({ linear: b.linearSlot, inspector: b.inspectorSlot })
    useTreeSelection.setState({ clear: () => b.clear() })
    if (tree.current) b.update(tree.current)
    setReady((n) => n + 1)
    return () => {
      setTreeSelection(null)
      useTreeSelection.setState({ clear: null })
      setSlots(null)
      b.destroy()
      board.current = null
    }
  }, [])

  useEffect(() => {
    if (!cfg || cfg.demo || !cfg.apiUrl) return
    let live = true
    fetchTree(cfg.apiUrl, cfg.token).then(
      (t) => {
        if (!live) return
        setError('')
        tree.current = t
        useLiveTree.setState({ tree: t })
        useTreeSelection.setState({ running: t.steps.filter((s) => !!s.run).length })
        board.current?.update(t)
      },
      (e) => live && setError(e instanceof Error ? e.message : String(e)),
    )
    return () => {
      live = false
    }
  }, [cfg, version, ready])

  return (
    <div className="tb-main-host">
      <div className="tb" ref={ref} style={{ ['--treew' as string]: `${treeWidth}px` }} />
      <div className="tb-treesplit" style={{ left: treeWidth }}>
        <Splitter side="left" onDrag={onDragTree} onDone={onDragDone} />
      </div>
      {slots && createPortal(<MyProgress workstreams={progress} onOpen={openProgress} cycles={false} />, slots.linear)}
      {slots && createPortal(<PanelAsk />, slots.inspector)}
      {wsSlot && createPortal(<MyProgress workstreams={progress} only={wsSlot.row.key} onOpen={openProgress} cycles={false} />, wsSlot.el)}
      {error && (
        <p className="tb-error" role="alert">
          Board unavailable: {error}
        </p>
      )}
    </div>
  )
}

/** SelectionChip: in the chat pane, what the board has open; × clears it. Each message carries it. */
export function SelectionChip() {
  const { sel, clear } = useTreeSelection()
  if (!sel) return null
  return (
    <div className="tb-chathead">
      <span className="tb-selchip" title="Each message to the Commander carries what you have open">
        <span className="tb-selchip-t">
          {sel.label} {sel.title}
        </span>
        <button type="button" aria-label="Clear the selection" onClick={() => (clear ? clear() : setTreeSelection(null))}>
          ×
        </button>
      </span>
    </div>
  )
}
