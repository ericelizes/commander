import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { sendAction, startAppShell } from './app-shell'
import type { DaemonDeps } from './cmdrd'
import * as ch from './shell-channels'

function fakeWindow() {
  const handlers: Record<string, (...a: unknown[]) => void> = {}
  return {
    handlers,
    on: vi.fn((ev: string, fn: () => void) => { handlers[ev] = fn }),
    maximize: vi.fn(),
    isMinimized: vi.fn(() => true),
    isMaximized: vi.fn(() => false),
    isDestroyed: vi.fn(() => false),
    restore: vi.fn(),
    show: vi.fn(),
    focus: vi.fn(),
    getNormalBounds: vi.fn(() => ({ x: 0, y: 0, width: 1200, height: 800 })),
    webContents: {
      on: vi.fn(),
      send: vi.fn(),
      reloadIgnoringCache: vi.fn(),
      toggleDevTools: vi.fn(),
    },
  }
}

function fakeElectron(platform: string, gotLock = true) {
  const appHandlers: Record<string, (...a: unknown[]) => void> = {}
  const ipcOn: Record<string, (...a: unknown[]) => void> = {}
  const app = {
    name: 'Commander',
    requestSingleInstanceLock: vi.fn(() => gotLock),
    quit: vi.fn(),
    on: vi.fn((ev: string, fn: () => void) => { appHandlers[ev] = fn }),
    whenReady: vi.fn(() => Promise.resolve()),
    getPath: vi.fn(() => mkdtempSync(join(tmpdir(), 'cmdr-shell-'))),
    setBadgeCount: vi.fn(),
    dock: platform === 'darwin' ? { setBadge: vi.fn(), setMenu: vi.fn() } : undefined,
  }
  const electron = {
    app,
    Menu: { buildFromTemplate: vi.fn((t: unknown) => ({ t })), setApplicationMenu: vi.fn() },
    ipcMain: {
      handle: vi.fn(),
      on: vi.fn((c: string, fn: () => void) => { ipcOn[c] = fn }),
    },
    shell: { openExternal: vi.fn() },
    screen: { getAllDisplays: vi.fn(() => [{ workArea: { x: 0, y: 0, width: 1512, height: 945 } }]) },
  }
  return { electron, app, appHandlers, ipcOn }
}

const onlineDeps: DaemonDeps = {
  platform: 'darwin',
  commanderHome: '/ch',
  home: '/h',
  uid: 1,
  readText: async () => 'http://127.0.0.1:8787',
  probe: async () => true,
  run: async () => {},
  lockHolderAlive: async () => true,
  spawnDetached: () => {},
  sleep: () => new Promise(() => {}), // park the watch loop
}

const flush = () => new Promise((r) => setTimeout(r, 0))

describe('startAppShell', () => {
  it('second instance quits and creates nothing', () => {
    const { electron, app } = fakeElectron('darwin', false)
    const createWindow = vi.fn()
    expect(startAppShell(electron as never, { isDev: false, createWindow, daemonDeps: onlineDeps })).toBeUndefined()
    expect(app.quit).toHaveBeenCalled()
    expect(createWindow).not.toHaveBeenCalled()
  })

  it('first instance builds the menu and window, and focuses on second-instance', async () => {
    const { electron, appHandlers } = fakeElectron('darwin')
    const win = fakeWindow()
    const shell = startAppShell(electron as never, {
      isDev: false,
      createWindow: () => win as never,
      daemonDeps: onlineDeps,
      platform: 'darwin',
    })!
    await flush()
    expect(electron.Menu.setApplicationMenu).toHaveBeenCalled()
    expect(shell.window()).toBe(win)
    expect(win.webContents.on).toHaveBeenCalledWith('before-input-event', expect.any(Function))
    expect(shell.daemonState()).toEqual({ status: 'online', url: 'http://127.0.0.1:8787' })
    expect(win.webContents.send).toHaveBeenCalledWith(ch.DAEMON_STATE, { status: 'online', url: 'http://127.0.0.1:8787' })

    appHandlers['second-instance']()
    expect(win.restore).toHaveBeenCalled()
    expect(win.focus).toHaveBeenCalled()
  })

  it('setRendererSource rebuilds the menu with the source item', async () => {
    const { electron } = fakeElectron('darwin')
    const shell = startAppShell(electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'darwin' })!
    await flush()
    const before = electron.Menu.setApplicationMenu.mock.calls.length
    shell.setRendererSource('live')
    shell.setRendererSource('live') // unchanged: no rebuild
    expect(electron.Menu.setApplicationMenu.mock.calls.length).toBe(before + 1)
    const labels = JSON.stringify(electron.Menu.buildFromTemplate.mock.calls.at(-1)![0])
    expect(labels).toContain('Renderer: Live (dev server)')
  })

  it('app:capture IPC captures the calling window', async () => {
    const { electron } = fakeElectron('darwin')
    startAppShell(electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'darwin' })
    await flush()
    const call = electron.ipcMain.handle.mock.calls.find((c) => c[0] === ch.APP_CAPTURE)!
    const sender = { capturePage: vi.fn(async () => ({ isEmpty: () => false, toPNG: () => Buffer.from('x') })) }
    expect(await (call[1] as (e: unknown) => Promise<string>)({ sender })).toBe(Buffer.from('x').toString('base64'))
  })

  it('dock IPC: macOS sets badge and a 5-item dock menu', async () => {
    const { electron, app, ipcOn } = fakeElectron('darwin')
    startAppShell(electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'darwin' })
    await flush()
    ipcOn[ch.DOCK_SET_NEEDS_YOU]({}, 3)
    expect(app.dock!.setBadge).toHaveBeenCalledWith('3')
    ipcOn[ch.DOCK_SET_WORKSTREAMS]({}, Array.from({ length: 8 }, (_, i) => ({ id: `${i}`, name: `w${i}` })))
    expect(app.dock!.setMenu).toHaveBeenCalled()
    const lastBuild = electron.Menu.buildFromTemplate.mock.calls.at(-1)![0] as unknown[]
    expect(lastBuild).toHaveLength(5)
  })

  it('dock IPC: Linux has no dock; badge goes to setBadgeCount', async () => {
    const { electron, app, ipcOn } = fakeElectron('linux')
    startAppShell(electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'linux' })
    await flush()
    ipcOn[ch.DOCK_SET_NEEDS_YOU]({}, 2)
    expect(app.setBadgeCount).toHaveBeenCalledWith(2)
    expect(() => ipcOn[ch.DOCK_SET_WORKSTREAMS]({}, [{ id: 'a', name: 'A' }])).not.toThrow()
  })

  it('quits when the last window closes on Linux, not on macOS', () => {
    const lin = fakeElectron('linux')
    startAppShell(lin.electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'linux' })
    lin.appHandlers['window-all-closed']()
    expect(lin.app.quit).toHaveBeenCalled()

    const mac = fakeElectron('darwin')
    startAppShell(mac.electron as never, { isDev: false, createWindow: () => fakeWindow() as never, daemonDeps: onlineDeps, platform: 'darwin' })
    mac.appHandlers['window-all-closed']()
    expect(mac.app.quit).not.toHaveBeenCalled()
  })
})

describe('sendAction', () => {
  it('maps actions to IPC sends and webContents calls', () => {
    const win = fakeWindow()
    const shell = { openExternal: vi.fn() }
    sendAction(win as never, shell, { type: 'expand-or-go', n: 4 })
    sendAction(win as never, shell, { type: 'next-workstream' })
    sendAction(win as never, shell, { type: 'reload' })
    sendAction(win as never, shell, { type: 'devtools' })
    sendAction(undefined, shell, { type: 'open-external', url: 'https://x' })
    expect(win.webContents.send).toHaveBeenCalledWith(ch.NAV_EXPAND_OR_GO, 4)
    expect(win.webContents.send).toHaveBeenCalledWith(ch.NAV_NEXT_WORKSTREAM)
    expect(win.webContents.reloadIgnoringCache).toHaveBeenCalled()
    expect(win.webContents.toggleDevTools).toHaveBeenCalled()
    expect(shell.openExternal).toHaveBeenCalledWith('https://x')
  })
})
