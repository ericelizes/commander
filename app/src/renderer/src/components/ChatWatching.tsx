// An agent between turns is still on the job: Operators work, CI runs,
// reviewers review, and cmdrd looks every minute and wakes it when something
// changes. This ends its chat with one quiet line saying so, shaped like a
// message row; opening it lists what is being watched. A line opens that
// Operator, moves the graph to the bead, or selects the workstream.

import { useMemo, useState } from 'react'
import { Binoculars, ChevronDown } from 'lucide-react'
import { trackedSince } from '../stateSince'
import { useStore } from '../store'
import { fmtAge, watchVM, type WatchItem, type WatchVM } from '../viewmodel'

export function ChatWatching({ role }: { role: string }) {
  const snapshot = useStore((s) => s.snapshot)
  const chats = useStore((s) => s.chats)
  const nowSec = useStore((s) => s.nowSec)
  const vm = useMemo(() => (snapshot ? watchVM(snapshot, role, chats, nowSec) : null), [snapshot, role, chats, nowSec])
  if (!vm) return null
  return (
    <WatchingView
      vm={vm}
      nowSec={nowSec}
      sinceOf={(i) => i.since ?? trackedSince(`watch:${i.key}`, i.what, nowSec)}
      onOpen={(i) => {
        const st = useStore.getState()
        if ('agent' in i.open) st.select({ kind: 'agent', key: i.open.agent, ws: i.open.ws })
        else if ('bead' in i.open) st.openLink(i.open.bead)
        else st.select({ kind: 'ws', key: i.open.ws })
      }}
    />
  )
}

/** ago reads seconds as "just now" / "1 min ago" / "2h ago". */
function ago(secs: number): string {
  if (secs < 60) return 'just now'
  if (secs < 3600) return `${Math.floor(secs / 60)} min ago`
  return `${fmtAge(secs)} ago`
}

/** inMin reads seconds ahead as "in 9 min". */
function inMin(secs: number): string {
  return secs < 60 ? 'in under a minute' : `in ${Math.round(secs / 60)} min`
}

export function WatchingView({
  vm,
  nowSec,
  sinceOf,
  onOpen,
  defaultOpen = false,
}: {
  vm: WatchVM
  nowSec: number
  sinceOf: (i: WatchItem) => number
  onOpen: (i: WatchItem) => void
  defaultOpen?: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  // "Watching: main CI · prod tag" with "checked 4 min ago" on the right (cmdrd's
  // last look, which wakes nobody), and "checking again in 9 min" when the
  // Manager asked for a check-in.
  const head = `Watching: ${vm.summary}`
  const next = [
    vm.checkedAt ? `checked ${ago(nowSec - vm.checkedAt)}` : '',
    vm.checkinAt && vm.checkinAt > nowSec ? `checking again ${inMin(vm.checkinAt - nowSec)}` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  const canOpen = vm.items.length > 0
  return (
    <div className="ct-activity" role="status">
      <button type="button" className={`ct-activity-head${open ? ' open' : ''}`} onClick={() => canOpen && setOpen(!open)} aria-expanded={canOpen ? open : undefined}>
        <Binoculars size={14} aria-hidden />
        {/* One sentence that wraps as a whole, never a column beside a column. */}
        <span className="cw-head">
          {head}
          {next && <span className="cw-next"> · {next}</span>}
        </span>
        {canOpen && <ChevronDown size={14} className="ct-chevron" />}
      </button>
      {open &&
        vm.items.map((i) => (
          <div key={i.key} className={`cw-line${i.slow ? ' slow' : ''}`}>
            <button type="button" onClick={() => onOpen(i)}>
              {i.title} · {i.what} · {fmtAge(nowSec - sinceOf(i))}
            </button>
          </div>
        ))}
    </div>
  )
}
