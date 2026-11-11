// TopBar runs across the window above the rail and the panes: Back and Forward
// on the left, then what each pane below it shows, lined up with that pane.

import { ArrowLeft, ArrowRight } from 'lucide-react'
import { isMac } from '../host'
import { useStore } from '../store'
import { useChatTarget } from './ChatSlot'
import { FocusHeader } from './FocusHeader'
import { modelName } from '../../../features/chat/modelName'
import { WorkstreamCommander } from './WorkstreamCommander'
import { wsKey } from '../viewmodel'
import { commanderScope } from '../jurisdiction'

export function TopBar({ left, chat }: { left: number; chat: number }) {
  const focus = useStore((s) => s.focus)
  const page = useStore((s) => s.selected.kind === 'progress')
  const focused = useStore((s) => s.selected.kind !== 'commander' && s.selected.kind !== 'progress')
  const narrow = useStore((s) => s.narrow)
  const chatTab = useStore((s) => s.chatTab)
  const target = useChatTarget()
  const chatTitle = target.title
  const model = useStore((s) => s.chats[target.role]?.model)
  const selected = useStore((s) => s.selected)
  const snapshot = useStore((s) => s.snapshot)
  const workflowOpen = useStore((s) => s.workflowOpen)
  const jurisdiction = (() => {
    const s = { selected, snapshot, workflowOpen }
    const key = s.selected.kind === 'ws' ? s.selected.key : s.selected.kind === 'agent' ? s.selected.ws : ''
    const ws = s.snapshot?.workstreams.find((w) => wsKey(w) === key)
    if (!ws) return null
    const row = s.snapshot?.display?.workstreams.find((r) => r.key === key)
    const track = (s.workflowOpen?.ws === key ? s.workflowOpen.track : undefined) ?? row?.scope
    const scope = commanderScope(ws, track)
    return scope ? { projectId: ws.project_id, beadId: scope.beadId, workstreamKey: key, owner: scope.owner } : null
  })()
  const split = focused && !narrow
  // A narrow window shows one pane at a time, so it names the one on screen.
  const showFocus = focused && (!narrow || !chatTab)
  const showChat = !focused || !narrow || chatTab
  return (
    <header className="app-top" style={{ gridTemplateColumns: split ? `${left}px minmax(0, 1fr) ${chat}px` : `${left}px minmax(0, 1fr)` }}>
      <div className="app-top-left">
        <HistoryArrows />
      </div>
      {page && (
        <div className="app-top-pane">
          <span className="app-top-title">Progress</span>
        </div>
      )}
      {showFocus && <div className="app-top-pane">{focus && <FocusHeader target={focus} />}{jurisdiction && <WorkstreamCommander key={`${jurisdiction.projectId}:${jurisdiction.beadId}`} {...jurisdiction} />}</div>}
      {showChat && !page && (
        <div className="app-top-pane app-top-chat">
          <span className="app-top-title">{chatTitle}</span>
          {model && <span className="app-top-model" title={model}>{modelName(model)}</span>}
        </div>
      )}
    </header>
  )
}

// HistoryArrows: Back and Forward through what you have looked at.
function HistoryArrows() {
  const back = useStore((s) => s.back.length > 0)
  const forward = useStore((s) => s.forward.length > 0)
  const mod = isMac() ? '⌘' : 'Ctrl+'
  return (
    <div className="history">
      <button type="button" aria-label="Back" title={`Back (${mod}[)`} disabled={!back} onClick={() => useStore.getState().goBack()}>
        <ArrowLeft size={16} strokeWidth={1.8} />
      </button>
      <button type="button" aria-label="Forward" title={`Forward (${mod}])`} disabled={!forward} onClick={() => useStore.getState().goForward()}>
        <ArrowRight size={16} strokeWidth={1.8} />
      </button>
    </div>
  )
}
