// The narrow bridge between the renderer and the Electron main process. The
// renderer also runs without it (served by cmdrd), so everything here is optional.
//
//   window.commanderHost   config, Cmd/Ctrl+n, smoke report, inspector webviews (.webview)
//   window.commanderShell  app shell: nav events, cmdrd health, dock badge/menu

import { contextBridge, ipcRenderer } from 'electron'
import type { CommanderHost, HostConfig } from '../shared/snapshot'
import { createInspectorWebviewBridge } from '../../electron/inspector/preload'
import { shellApi } from '../../electron/shell-preload'
import { NAV_EXPAND_OR_GO } from '../../electron/shell-channels'

const host: CommanderHost = {
  platform: process.platform,
  getConfig: () => ipcRenderer.invoke('cmdr:config') as Promise<HostConfig | null>,
  onShortcut(cb) {
    const listener = (_e: unknown, n: number) => cb(n)
    ipcRenderer.on(NAV_EXPAND_OR_GO, listener)
    return () => ipcRenderer.removeListener(NAV_EXPAND_OR_GO, listener)
  },
  rendered: (report) => ipcRenderer.send('cmdr:rendered', report),
  webview: createInspectorWebviewBridge(),
}

contextBridge.exposeInMainWorld('commanderHost', host)
contextBridge.exposeInMainWorld('commanderShell', shellApi(ipcRenderer))
