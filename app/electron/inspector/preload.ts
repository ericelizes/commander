// The preload half of the inspector bridge. The host preload exposes it as
// window.commanderHost.webview:
//
//   contextBridge.exposeInMainWorld('commanderHost', { ...yours, webview: createInspectorWebviewBridge() })
//
// Its absence is how the renderer knows it runs as a plain web page.

import { ipcRenderer } from 'electron'

import { CH, type CaptureReq, type NavMsg, type ShortcutMsg, type SyncMsg, type ViewState } from './contract'

export function createInspectorWebviewBridge() {
  const listen = <T>(channel: string, cb: (v: T) => void) => {
    const h = (_e: unknown, v: T) => cb(v)
    ipcRenderer.on(channel, h)
    return () => {
      ipcRenderer.off(channel, h)
    }
  }
  return {
    sync: (msg: SyncMsg) => ipcRenderer.send(CH.sync, msg),
    nav: (msg: NavMsg) => ipcRenderer.send(CH.nav, msg),
    openExternal: (url: string) => ipcRenderer.send(CH.openExternal, url),
    capture: (req: CaptureReq) => ipcRenderer.invoke(CH.capture, req),
    signinStatus: (): Promise<Record<string, boolean>> => ipcRenderer.invoke(CH.signin),
    onState: (cb: (s: ViewState) => void) => listen(CH.state, cb),
    onShortcut: (cb: (s: ShortcutMsg) => void) => listen(CH.shortcut, cb),
  }
}
