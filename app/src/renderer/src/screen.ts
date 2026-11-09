// screenContext describes what the user's app shows, for the agent they are
// messaging: what the right panel shows, which tabs are open, and which board
// version is on screen. The board itself reaches Commander and the Managers
// through their turn hook (cmdrd's board files), so it is not repeated here.

import type { TabInfo } from '../../features/inspector/types'
import { useStore } from './store'
import { askKey, selectionKey, wsKey } from './viewmodel'

let panel = { tabs: [] as string[], showing: 'Graph' }

/** setPanel records the right panel's tabs and which one shows (InspectorColumn calls it). */
export function setPanel(tabs: TabInfo[], showing: string) {
  panel = { tabs: tabs.map((t) => (t.target && t.target !== t.title ? `${t.title} (${t.target})` : t.title)), showing }
}

export function screenContext(role: string): string {
  const st = useStore.getState()
  const snap = st.snapshot
  if (!snap) return ''
  // The graph shows the selected workstream (all of them for Commander), or
  // where a chat link moved it.
  const sel = st.selected
  const linked = st.graphFocus && st.graphFocus.sel === selectionKey(sel) ? st.graphFocus : null
  let focus = linked?.ws ?? (sel.kind === 'ws' ? sel.key : sel.kind === 'agent' ? sel.ws : undefined)
  if (!focus && sel.kind === 'ask') {
    const a = snap.needs_you.find((n) => askKey(n) === sel.key)
    const w = a && snap.workstreams.find((x) => x.project === a.project && x.title === a.workstream)
    focus = w ? wsKey(w) : undefined
  }
  const focusWs = snap.workstreams.find((w) => wsKey(w) === focus)
  const graph = focusWs ? `the ${focusWs.title} workstream${linked?.bead ? `, centred on bead ${linked.bead}` : ''}` : 'every workstream'

  const out = ["What the user's Commander app shows as they send this (added by the app; they did not type it):"]
  out.push(`- Right panel: ${panel.showing === 'Graph' ? `Graph tab, showing ${graph}` : `the "${panel.showing}" tab`}`)
  out.push(`- Tabs open: Graph${panel.tabs.length ? `, ${panel.tabs.join(', ')}` : ''}`)
  // Operators get no board; Commander and the Managers read theirs in the turn hook.
  const version = role === 'commander' || role.startsWith('manager-') ? snap.board_versions?.[role] : undefined
  if (version) out.push(`- Board on screen: v${version} (the version in your board context)`)
  return out.join('\n')
}
