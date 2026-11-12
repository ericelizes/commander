// startAppShell is the desktop behavior around the main window: single
// instance, saved window bounds, menu, in-app shortcuts, dock badge/menu,
// and the cmdrd health check. main.ts owns how the window is built and
// calls this once:
//
//   import * as electron from 'electron'
//   startAppShell(electron, { isDev: !electron.app.isPackaged, createWindow })

import type * as Electron from 'electron'
import type { BrowserWindow } from 'electron'
import { buildMenuTemplate, type MenuAction } from './menu'
import { attachShortcuts } from './shortcuts'
import { dockMenuTemplate, setNeedsYouBadge } from './dock'
import { loadWindowState, trackWindowState, windowStateFile, type WindowState } from './window-state'
import { nodeDaemonDeps, watchDaemon, type DaemonDeps, type DaemonState } from './cmdrd'
import { capturePng } from './capture'
import * as ch from './shell-channels'
import type { RendererSource } from './renderer-source'

type ElectronMain = Pick<typeof Electron, 'app' | 'Menu' | 'ipcMain' | 'shell' | 'screen'>

export interface AppShellOptions {
  isDev: boolean
  // Build (and load) the main window at the saved bounds. The shell
  // maximizes it afterwards when the saved state was maximized.
  createWindow: (state: WindowState) => BrowserWindow
  daemonDeps?: DaemonDeps
  platform?: string
  // Pair this Mac as a place (App menu item); undefined = none.
  pairPlace?: () => void
}

export interface AppShell {
  window: () => BrowserWindow | undefined
  daemonState: () => DaemonState
  // The window now shows the live dev server or the bundle: the View menu says which.
  setRendererSource: (s: RendererSource) => void
}

// sendAction turns a menu/shortcut action into what the window should do.
export function sendAction(
  win: BrowserWindow | undefined,
  shell: Pick<typeof Electron.shell, 'openExternal'>,
  action: MenuAction,
): void {
  if (action.type === 'open-external') {
    void shell.openExternal(action.url)
    return
  }
  if (!win || win.isDestroyed()) return
  const wc = win.webContents
  switch (action.type) {
    case 'expand-or-go': wc.send(ch.NAV_EXPAND_OR_GO, action.n); break
    case 'quick-switcher': wc.send(ch.NAV_QUICK_SWITCHER); break
    case 'next-workstream': wc.send(ch.NAV_NEXT_WORKSTREAM); break
    case 'prev-workstream': wc.send(ch.NAV_PREV_WORKSTREAM); break
    case 'settings': wc.send(ch.NAV_SETTINGS); break
    case 'reload': wc.reloadIgnoringCache(); break
    case 'devtools': wc.toggleDevTools(); break
  }
}

export function focusWindow(win: BrowserWindow | undefined): void {
  if (!win || win.isDestroyed()) return
  if (win.isMinimized()) win.restore()
  win.show()
  win.focus()
}

// startAppShell returns undefined in a second instance (which quits after
// handing focus to the first).
export function startAppShell(electron: ElectronMain, opts: AppShellOptions): AppShell | undefined {
  const { app, Menu, ipcMain, shell, screen } = electron
  const platform = opts.platform ?? process.platform
  if (!app.requestSingleInstanceLock()) {
    app.quit()
    return undefined
  }

  let win: BrowserWindow | undefined
  let daemon: DaemonState = { status: 'checking' }
  let rendererSource: RendererSource | undefined
  let ready = false
  const dispatch = (action: MenuAction) => sendAction(win, shell, action)

  const openWindow = () => {
    const file = windowStateFile(app.getPath('userData'))
    const state = loadWindowState(file, screen.getAllDisplays().map((d) => d.workArea))
    win = opts.createWindow(state)
    if (state.maximized) win.maximize()
    trackWindowState(win, file)
    attachShortcuts(win.webContents, { platform, isDev: opts.isDev }, dispatch)
    // A reloaded renderer asks with daemon:get-state; push it too.
    win.webContents.on('did-finish-load', () => win?.webContents.send(ch.DAEMON_STATE, daemon))
    win.on('closed', () => {
      win = undefined
    })
  }

  app.on('second-instance', () => {
    if (!win) openWindow()
    focusWindow(win)
  })

  ipcMain.handle(ch.DAEMON_GET_STATE, () => daemon)
  ipcMain.handle(ch.APP_CAPTURE, (e) => capturePng(e.sender))
  ipcMain.on(ch.DOCK_SET_NEEDS_YOU, (_e, count: number) => setNeedsYouBadge(app, platform, count))
  ipcMain.on(ch.DOCK_SET_WORKSTREAMS, (_e, list: ch.DockWorkstream[]) => {
    if (platform !== 'darwin' || !app.dock) return
    const open = (id: string) => {
      if (!win) openWindow()
      focusWindow(win)
      win?.webContents.send(ch.NAV_OPEN_WORKSTREAM, id)
    }
    app.dock.setMenu(Menu.buildFromTemplate(dockMenuTemplate(Array.isArray(list) ? list : [], open)))
  })

  const setMenu = () => {
    Menu.setApplicationMenu(Menu.buildFromTemplate(buildMenuTemplate({
      platform,
      isDev: opts.isDev,
      appName: app.name,
      dispatch,
      rendererSource,
      pairPlace: opts.pairPlace,
    })))
  }

  void app.whenReady().then(() => {
    ready = true
    setMenu()
    openWindow()
    watchDaemon(opts.daemonDeps ?? nodeDaemonDeps(), (s) => {
      daemon = s
      win?.webContents.send(ch.DAEMON_STATE, s)
    })
  })

  app.on('activate', () => {
    if (!win) openWindow() // macOS: dock click with no window open
  })
  app.on('window-all-closed', () => {
    if (platform !== 'darwin') app.quit()
  })

  return {
    window: () => win,
    daemonState: () => daemon,
    setRendererSource(s) {
      if (s === rendererSource) return
      rendererSource = s
      if (ready) setMenu()
    },
  }
}
