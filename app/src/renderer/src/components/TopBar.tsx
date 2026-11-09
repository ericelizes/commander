// TopBar runs across the window above the rail and the panes: Back and Forward
// on the left, then what each pane below it shows, lined up with that pane.

import { ArrowLeft, ArrowRight } from 'lucide-react'
import { isMac } from '../host'
import { useStore } from '../store'
import { useChatTarget } from './ChatSlot'
import { FocusHeader } from './FocusHeader'

export function TopBar({ left, chat }: { left: number; chat: number }) {
  const focus = useStore((s) => s.focus)
  const page = useStore((s) => s.selected.kind === 'progress')
  const focused = useStore((s) => s.selected.kind !== 'commander' && s.selected.kind !== 'progress')
  const narrow = useStore((s) => s.narrow)
  const chatTab = useStore((s) => s.chatTab)
  const chatTitle = useChatTarget().title
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
      {showFocus && <div className="app-top-pane">{focus && <FocusHeader target={focus} />}</div>}
      {showChat && !page && (
        <div className="app-top-pane">
          <span className="app-top-title">{chatTitle}</span>
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
