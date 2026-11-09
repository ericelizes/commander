// useShell connects the renderer to the Electron app shell (window.commanderShell):
// nav events from the menu/shortcuts/dock, cmdrd health, and the dock badge
// (NEEDS YOU count) and dock menu (workstreams). A no-op in a plain browser.
// Cmd/Ctrl+0..9 arrive through host.onShortcut (App), which follows the
// store's expand-or-go rule.

import { useEffect } from 'react'
import { shell } from './host'
import { useStore } from './store'
import { wsKey } from './viewmodel'

export function useShell(): void {
  useEffect(() => {
    if (!shell) return
    const st = () => useStore.getState()
    const offNav = shell.onNav((ev) => {
      switch (ev.type) {
        case 'next-workstream':
          st().stepWorkstream(1)
          break
        case 'prev-workstream':
          st().stepWorkstream(-1)
          break
        case 'open-workstream':
          st().goWorkstream(ev.id)
          break
        // expand-or-go comes through host.onShortcut; the quick switcher and
        // settings have no UI yet.
      }
    })
    const onDaemon = (s: { status: string }) => {
      useStore.setState({ daemon: s.status })
      // cmdrd is back: retry the WebSocket now instead of waiting out the backoff.
      if (s.status === 'online' && st().conn === 'offline') st().reconnectNow?.()
    }
    void shell.getDaemonState().then(onDaemon, () => {})
    const offDaemon = shell.onDaemonState(onDaemon)
    return () => {
      offNav()
      offDaemon()
    }
  }, [])

  const needs = useStore((s) => s.snapshot?.needs_you?.length ?? 0)
  useEffect(() => shell?.setNeedsYou(needs), [needs])

  // A string, so a new snapshot with the same workstreams sends nothing.
  const dockJSON = useStore((s) => JSON.stringify((s.snapshot?.workstreams ?? []).map((w) => ({ id: wsKey(w), name: `${w.n}  ${w.title}` }))))
  useEffect(() => shell?.setDockWorkstreams(JSON.parse(dockJSON)), [dockJSON])
}
