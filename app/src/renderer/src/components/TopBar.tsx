// TopBar runs across the window above the rail and the panes: Back and Forward
// on the left, then what each pane below it shows, lined up with that pane.

import { ArrowLeft, ArrowRight } from 'lucide-react'
import { isMac } from '../host'
import { useStore } from '../store'
import { useChatTarget } from './ChatSlot'
import { modelName } from '../../../features/chat/modelName'
import { isPage } from '../viewmodel'

export function TopBar({ left, chat }: { left: number; chat: number }) {
  const page = useStore((s) => (isPage(s.selected) ? s.selected.kind : null))
  const focus = useStore((s) => s.focus)
  // The middle pane is the tree board, always shown beside the chat (as in App), so the bar is always split.
  const focused = true
  const narrow = useStore((s) => s.narrow)
  const target = useChatTarget()
  const chatTitle = target.title
  const model = useStore((s) => s.chats[target.role]?.model)
  const split = focused && !narrow
  // A narrow window shows one pane at a time, so it names the one on screen.
  // In a narrow window the chat is a popup with its own close, so the bar always names the board and only names the chat when it sits beside it.
  const showFocus = focused
  const showChat = !narrow
  return (
    <header className="app-top" style={{ gridTemplateColumns: split ? `${left}px minmax(0, 1fr) ${chat}px` : `${left}px minmax(0, 1fr)` }}>
      <div className="app-top-left">
        <HistoryArrows />
      </div>
      {page && (
        <div className="app-top-pane">
          <span className="app-top-title">{page === 'workflows' ? 'Workflows' : page === 'brief' ? 'Brief' : 'Progress'}</span>
        </div>
      )}
      {showFocus && <div className="app-top-pane">{focus?.n != null && <span className="app-top-label">Workstream {focus.n}{focus.letter ?? ''}</span>}</div>}
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
