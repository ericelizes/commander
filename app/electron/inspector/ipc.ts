// registerInspectorIpc wires the inspector's webviews into a host window:
// the persistent partition (UA, permissions), the views manager and the IPC
// channels the preload bridge talks to. Works on macOS, Linux and Windows.

import { app, BrowserWindow, dialog, ipcMain, session as electronSession, shell, type IpcMainEvent } from 'electron'

import { CH, type CaptureReq, type NavMsg, type SyncMsg } from './contract'
import { chromeUserAgent, PARTITION, permissionAllowed, signedIn } from './policy'
import { askInWindow, providerGate, trustFile } from './trust'
import { InspectorViews } from './views'

export type InspectorDeps = {
  dev?: boolean // enables devtools on webviews
  openExternal?: (url: string) => void // default: shell.openExternal
}

export type InspectorHandle = { views: InspectorViews; dispose: () => void }

let partitionReady = false

// setupPartition configures persist:cockpit once per process.
export function setupPartition(): Electron.Session {
  const ses = electronSession.fromPartition(PARTITION)
  if (!partitionReady) {
    partitionReady = true
    ses.setUserAgent(chromeUserAgent(process.platform, process.versions.chrome ?? ''))
    ses.setPermissionRequestHandler((_wc, permission, callback) => callback(permissionAllowed(permission)))
    ses.setPermissionCheckHandler((_wc, permission) => permissionAllowed(permission))
  }
  return ses
}

export function registerInspectorIpc(win: BrowserWindow, deps: InspectorDeps = {}): InspectorHandle {
  const ses = setupPartition()
  const openExternal =
    deps.openExternal ??
    ((url: string) => {
      if (/^(https?|mailto):/i.test(url)) void shell.openExternal(url)
    })
  const send = (channel: string, payload: unknown) => {
    if (!win.isDestroyed() && !win.webContents.isDestroyed()) win.webContents.send(channel, payload)
  }
  const views = new InspectorViews(win, {
    session: ses,
    dev: deps.dev ?? false,
    openExternal,
    // Sync on purpose: see ProviderGate. It holds the main process only for this
    // one question, once per site and provider.
    providerGate: providerGate(trustFile(app.getPath('userData')), askInWindow(win, (wc) => BrowserWindow.fromWebContents(wc), (parent, options) => dialog.showMessageBoxSync(parent, options))),
    emitState: (s) => send(CH.state, s),
    emitShortcut: (s) => send(CH.shortcut, s),
  })

  // Only this window's renderer may drive its views.
  const fromHost = (e: { sender: Electron.WebContents }) => e.sender === win.webContents
  const onSync = (e: IpcMainEvent, msg: SyncMsg) => fromHost(e) && views.sync(msg)
  const onNav = (e: IpcMainEvent, msg: NavMsg) => fromHost(e) && views.nav(msg)
  const onExternal = (e: IpcMainEvent, url: string) => fromHost(e) && typeof url === 'string' && openExternal(url)
  ipcMain.on(CH.sync, onSync)
  ipcMain.on(CH.nav, onNav)
  ipcMain.on(CH.openExternal, onExternal)
  ipcMain.handle(CH.signin, async (e) => {
    if (!fromHost(e)) return {}
    return signedIn(await ses.cookies.get({}))
  })

  ipcMain.handle(CH.capture, async (e, req: CaptureReq) => {
    if (!fromHost(e) || !req || typeof req.id !== 'number') return { ok: false, reason: 'failed' }
    const block = Array.isArray(req.block) ? req.block.filter((b) => typeof b === 'string') : []
    return views.capture(req.id, block, typeof req.expect === 'string' ? req.expect : undefined)
  })

  const dispose = () => {
    ipcMain.removeHandler(CH.capture)
    ipcMain.off(CH.sync, onSync)
    ipcMain.off(CH.nav, onNav)
    ipcMain.off(CH.openExternal, onExternal)
    ipcMain.removeHandler(CH.signin)
    views.dispose()
  }
  win.on('closed', dispose)
  return { views, dispose }
}
