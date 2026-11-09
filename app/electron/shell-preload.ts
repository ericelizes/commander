// The renderer's side of the app shell, for the preload script:
//
//   import { contextBridge, ipcRenderer } from 'electron'
//   contextBridge.exposeInMainWorld('commanderShell', shellApi(ipcRenderer))
//
// The renderer then uses window.commanderShell (type: ShellApi).

import type { IpcRenderer } from 'electron'
import type { DaemonState } from './cmdrd'
import type { RemoteUpdateResult, UpdateAction } from './remote-update'
import * as ch from './shell-channels'

export type NavEvent =
  | { type: 'expand-or-go', n: number }
  | { type: 'quick-switcher' }
  | { type: 'next-workstream' }
  | { type: 'prev-workstream' }
  | { type: 'settings' }
  | { type: 'open-workstream', id: string }

export interface ShellApi {
  onNav: (cb: (ev: NavEvent) => void) => () => void
  onDaemonState: (cb: (s: DaemonState) => void) => () => void
  getDaemonState: () => Promise<DaemonState>
  setNeedsYou: (count: number) => void
  setDockWorkstreams: (list: ch.DockWorkstream[]) => void
  /** A base64 PNG of this window, taken by Electron itself (no Screen Recording permission). */
  capturePage: () => Promise<string>
  /** Install or roll back over SSH after this app's own confirmation dialog; lines stream to onLine. */
  runUpdate: (action: UpdateAction, onLine: (line: string) => void) => Promise<RemoteUpdateResult>
}

export function shellApi(ipc: Pick<IpcRenderer, 'on' | 'removeListener' | 'invoke' | 'send'>): ShellApi {
  const subscribe = (channel: string, fn: (...args: any[]) => void) => {
    const listener = (_e: unknown, ...args: any[]) => fn(...args)
    ipc.on(channel, listener)
    return () => {
      ipc.removeListener(channel, listener)
    }
  }
  return {
    onNav: (cb) => {
      const offs = [
        subscribe(ch.NAV_EXPAND_OR_GO, (n: number) => cb({ type: 'expand-or-go', n })),
        subscribe(ch.NAV_QUICK_SWITCHER, () => cb({ type: 'quick-switcher' })),
        subscribe(ch.NAV_NEXT_WORKSTREAM, () => cb({ type: 'next-workstream' })),
        subscribe(ch.NAV_PREV_WORKSTREAM, () => cb({ type: 'prev-workstream' })),
        subscribe(ch.NAV_SETTINGS, () => cb({ type: 'settings' })),
        subscribe(ch.NAV_OPEN_WORKSTREAM, (id: string) => cb({ type: 'open-workstream', id })),
      ]
      return () => offs.forEach((off) => off())
    },
    onDaemonState: (cb) => subscribe(ch.DAEMON_STATE, cb),
    getDaemonState: () => ipc.invoke(ch.DAEMON_GET_STATE),
    setNeedsYou: (count) => ipc.send(ch.DOCK_SET_NEEDS_YOU, count),
    setDockWorkstreams: (list) => ipc.send(ch.DOCK_SET_WORKSTREAMS, list),
    capturePage: () => ipc.invoke(ch.APP_CAPTURE),
    runUpdate: async (action, onLine) => {
      const off = subscribe(ch.UPDATE_REMOTE_LINE, (line: string) => onLine(String(line)))
      try {
        return (await ipc.invoke(ch.UPDATE_REMOTE_RUN, action === 'rollback' ? 'rollback' : 'install')) as RemoteUpdateResult
      } finally {
        off()
      }
    },
  }
}
