// Electron main: one window, the cmdrd URL + token for the renderer, and the
// app-wide Ctrl/Cmd+0..9 keys. All cockpit state comes from cmdrd.
//
// Flags (used by `npm run smoke:electron`):
//   --smoke            hidden run: render, write a PNG, exit 0 (rendered as expected) or 1
//   --offline          expect the offline state (pair with --fixture and a dead COMMANDER_API_URL)
//   --fixture <file>   a snapshot to show before the live feed
//   --out <file>       PNG path (default out/smoke.png)

import { hostname } from 'node:os'
import * as electron from 'electron'
import { app, BrowserWindow, dialog, ipcMain, nativeTheme, session, shell } from 'electron'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { apiConfig, apiRequestOrigin, commanderHome } from './config'
import type { HostConfig, Snapshot } from '../shared/snapshot'
import { startAppShell, type AppShell } from '../../electron/app-shell'
import { devRendererURL, sourceLabel, watchRendererSource, type RendererSource } from '../../electron/renderer-source'
import { appLogPath, createAppLog, logProcess, logWindow } from '../../electron/app-log'
import type { WindowState } from '../../electron/window-state'
import { registerInspectorIpc } from '../../electron/inspector'
import { attachContextMenu } from '../../electron/context-menu'
import { registerRemoteUpdateIpc, remoteSettings, runSsh, type RemoteUpdateStatus } from '../../electron/remote-update'
import { startPlaces, pairThisMac, type PlaceMainDeps } from '../../electron/place-main'

const isMac = process.platform === 'darwin'
const argv = process.argv
const flag = (name: string) => argv.includes(name)
const arg = (name: string) => {
  const i = argv.indexOf(name)
  return i >= 0 ? argv[i + 1] : undefined
}
const smoke = flag('--smoke')
const expectOffline = flag('--offline')
const fixturePath = arg('--fixture')
const outPath = resolve(arg('--out') ?? 'out/smoke.png')

// What went wrong, for "the app broke" reports: ~/.commander/logs/app.log.
const log = createAppLog(appLogPath(commanderHome()))
logProcess(process, log)

// Test runs never show a window or a Dock icon.
if (smoke && isMac) app.dock?.hide()

function hostConfig(): HostConfig {
  const cfg: HostConfig = { ...apiConfig(), smoke, localHost: hostname() }
  if (fixturePath) cfg.fixture = JSON.parse(readFileSync(resolve(fixturePath), 'utf8')) as Snapshot
  return cfg
}

function createWindow(state?: WindowState): BrowserWindow {
  const win = new BrowserWindow({
    width: state?.width ?? 1440,
    height: state?.height ?? 900,
    ...(state?.x !== undefined && state?.y !== undefined ? { x: state.x, y: state.y } : {}),
    minWidth: 800,
    minHeight: 500,
    show: false,
    title: 'Commander',
    // macOS: a transparent window over vibrancy. Opaque themes paint over it;
    // the glass theme leaves the page transparent so the blur shows through.
    ...(isMac
      ? { titleBarStyle: 'hiddenInset' as const, trafficLightPosition: { x: 18, y: 18 }, vibrancy: 'under-window' as const, visualEffectState: 'active' as const, backgroundColor: '#00000000' }
      : { autoHideMenuBar: true, backgroundColor: nativeTheme.shouldUseDarkColors ? '#131418' : '#ecedf0' }),
    webPreferences: {
      preload: join(__dirname, '../preload/index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      spellcheck: false,
    },
  })

  // No remote content: external links open in the default browser, nothing navigates the window.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:/.test(url)) void shell.openExternal(url)
    return { action: 'deny' }
  })
  win.webContents.on('will-navigate', (e, url) => {
    if (url !== win.webContents.getURL()) e.preventDefault()
  })

  attachContextMenu(win.webContents)
  logWindow(win, win.webContents, log)
  if (!smoke) win.once('ready-to-show', () => win.show())
  const bundle = join(__dirname, '../../dist/index.html')
  const devUrl = devRendererURL()
  if (smoke) void win.loadFile(bundle)
  else if (process.env.ELECTRON_RENDERER_URL) void win.loadURL(process.env.ELECTRON_RENDERER_URL) // electron-vite dev (`cmdr app dev`)
  else if (!devUrl) void win.loadFile(bundle)
  else {
    // The live renderer dev server while it answers (HMR: edits under app/src
    // apply in seconds), else the bundle; re-checked every 3s.
    const onChange = (s: RendererSource) => {
      log('info', `renderer: ${sourceLabel(s)}`)
      appShell?.setRendererSource(s)
    }
    const watch = watchRendererSource(win, { devUrl, bundle, onChange })
    win.on('closed', () => watch.stop())
  }
  return win
}

let appShell: AppShell | undefined

/** cmdrd only accepts loopback Origins: see apiRequestOrigin. */
function allowApiOrigin() {
  const { apiUrl } = apiConfig()
  const u = new URL(apiUrl)
  const filter = { urls: [`ws://${u.host}/*`, `http://${u.host}/*`] }
  session.defaultSession.webRequest.onBeforeSendHeaders(filter, (details, cb) => {
    const h = details.requestHeaders
    const origin = h.Origin ?? h.origin
    const want = apiRequestOrigin(origin, u.origin)
    cb({ requestHeaders: want === origin ? h : { ...h, Origin: want } })
  })
  session.defaultSession.setPermissionRequestHandler((_wc, _perm, cb) => cb(false))
}

function runSmoke(win: BrowserWindow) {
  const timer = setTimeout(() => {
    console.error('smoke: timed out waiting for the first render')
    app.exit(1)
  }, 20_000)
  ipcMain.once('cmdr:rendered', async (_e, report: { ok: boolean; detail: string }) => {
    clearTimeout(timer)
    const img = await win.webContents.capturePage(undefined, { stayHidden: true })
    mkdirSync(dirname(outPath), { recursive: true })
    writeFileSync(outPath, img.toPNG())
    const pass = expectOffline ? !report.ok && report.detail === 'cmdrd offline' : report.ok
    console.log(`smoke: ${report.detail} -> ${outPath} (${pass ? 'pass' : 'FAIL'})`)
    app.exit(pass ? 0 : 1)
  })
}

ipcMain.handle('cmdr:config', () => hostConfig())

// Install over SSH when cmdrd cannot show its own confirmation (headless or on
// another machine); the dialog is this app's, on this screen.
registerRemoteUpdateIpc(ipcMain, {
  settings: () => remoteSettings(commanderHome()),
  fetchStatus: async () => {
    const { apiUrl, token } = apiConfig()
    const res = await fetch(`${apiUrl}/api/update?fresh=1`, { headers: { Authorization: `Bearer ${token}` } })
    if (!res.ok) throw new Error(`cmdrd answered ${res.status}`)
    return (await res.json()) as RemoteUpdateStatus
  },
  confirm: async (wc, message, detail) => {
    const opts = { type: 'warning' as const, title: 'Commander update', message, detail, buttons: ['Cancel', 'Install'], defaultId: 0, cancelId: 0, noLink: true }
    const win = wc ? BrowserWindow.fromWebContents(wc) : null
    const { response } = win ? await dialog.showMessageBox(win, opts) : await dialog.showMessageBox(opts)
    return response === 1
  },
  run: (args, onLine) => runSsh(args, onLine),
})

if (smoke) {
  app.whenReady().then(() => {
    allowApiOrigin()
    runSmoke(createWindow())
  })
  app.on('window-all-closed', () => app.quit())
} else {
  // The shell owns the app lifecycle: single instance, menu, shortcuts
  // (Cmd/Ctrl+0..9 -> nav:expand-or-go), window state, dock badge, cmdrd health.
  void app.whenReady().then(allowApiOrigin)
  // This Mac as a place for governed Operators (docs/plans/2026-11-09-mac-operator.md):
  // only once paired, and every run asks the user here first.
  const placeDeps: PlaceMainDeps = {
    app, dialog, safeStorage: electron.safeStorage, commanderHome: commanderHome(),
    api: () => { const { apiUrl, token } = apiConfig(); return { apiUrl, token } },
    log: (m) => log('info', m),
  }
  void app.whenReady().then(() => startPlaces(placeDeps))
  appShell = startAppShell(electron, {
    isDev: !app.isPackaged,
    pairPlace: isMac ? () => void pairThisMac(placeDeps).catch((e) => log('info', `place: pairing failed: ${(e as Error).message}`)) : undefined,
    createWindow: (state) => {
      const win = createWindow(state)
      registerInspectorIpc(win, { dev: !app.isPackaged })
      return win
    },
  })
}
