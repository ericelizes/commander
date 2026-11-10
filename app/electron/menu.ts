// The application menu as a plain template (Menu.buildFromTemplate it in
// main). macOS gets the App menu and window roles; Linux gets a File menu
// and no macOS-only roles.
//
// Items that shortcuts.ts also matches carry registerAccelerator: false:
// the accelerator is shown, but before-input-event does the work, so a key
// never fires twice and still works while a terminal/webview has focus.

import type { MenuItemConstructorOptions } from 'electron'
import { acceleratorFor, type ShortcutAction } from './shortcuts'
import { sourceLabel, type RendererSource } from './renderer-source'

export const REPO_URL = 'https://github.com/example-user/commander'

export type MenuAction = ShortcutAction | { type: 'open-external', url: string }

export interface MenuOptions {
  platform: string
  isDev: boolean
  appName: string
  dispatch: (action: MenuAction) => void
  // Where the page comes from (the live dev server or the bundle): shown in
  // View; undefined = not known yet / not watched (smoke, electron-vite dev).
  rendererSource?: RendererSource
  // Pair this Mac as a place for governed Operators (macOS); undefined = no item.
  pairPlace?: () => void
}

export function buildMenuTemplate(opts: MenuOptions): MenuItemConstructorOptions[] {
  const mac = opts.platform === 'darwin'
  const item = (label: string, action: ShortcutAction): MenuItemConstructorOptions => ({
    label,
    accelerator: acceleratorFor(action, opts.platform),
    registerAccelerator: false,
    click: () => opts.dispatch(action),
  })
  const settings = item(mac ? 'Settings…' : 'Settings', { type: 'settings' })

  const appMenu: MenuItemConstructorOptions = {
    label: opts.appName,
    submenu: [
      { role: 'about' },
      { type: 'separator' },
      settings,
      ...(opts.pairPlace ? [{ label: 'Pair this Mac as a place…', click: opts.pairPlace } as MenuItemConstructorOptions] : []),
      { type: 'separator' },
      { role: 'services' },
      { type: 'separator' },
      { role: 'hide' },
      { role: 'hideOthers' },
      { role: 'unhide' },
      { type: 'separator' },
      { role: 'quit' },
    ],
  }
  const fileMenu: MenuItemConstructorOptions = {
    label: 'File',
    submenu: [settings, { type: 'separator' }, { role: 'quit' }],
  }

  const editMenu: MenuItemConstructorOptions = {
    label: 'Edit',
    submenu: [
      { role: 'undo' },
      { role: 'redo' },
      { type: 'separator' },
      { role: 'cut' },
      { role: 'copy' },
      { role: 'paste' },
      ...(mac ? [{ role: 'pasteAndMatchStyle' } as MenuItemConstructorOptions] : []),
      { role: 'delete' },
      { role: 'selectAll' },
    ],
  }

  const workstreams: MenuItemConstructorOptions[] = [
    item('Commander', { type: 'expand-or-go', n: 0 }),
    ...[1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => item(`Workstream ${n}`, { type: 'expand-or-go', n })),
  ]
  const devItems: MenuItemConstructorOptions[] = [
    item('Reload', { type: 'reload' }),
    ...(opts.isDev ? [item('Toggle Developer Tools', { type: 'devtools' })] : []),
    { type: 'separator' },
  ]
  const sourceItems: MenuItemConstructorOptions[] = opts.rendererSource
    ? [{ label: `Renderer: ${sourceLabel(opts.rendererSource)}`, enabled: false }, { type: 'separator' }]
    : []
  const viewMenu: MenuItemConstructorOptions = {
    label: 'View',
    submenu: [
      ...sourceItems,
      item('Quick Switcher…', { type: 'quick-switcher' }),
      item('Next Workstream', { type: 'next-workstream' }),
      item('Previous Workstream', { type: 'prev-workstream' }),
      { type: 'separator' },
      ...workstreams,
      { type: 'separator' },
      ...devItems,
      { role: 'resetZoom' },
      { role: 'zoomIn' },
      { role: 'zoomOut' },
      { type: 'separator' },
      { role: 'togglefullscreen' },
    ],
  }

  const windowMenu: MenuItemConstructorOptions = {
    label: 'Window',
    submenu: mac
      ? [{ role: 'minimize' }, { role: 'zoom' }, { type: 'separator' }, { role: 'front' }]
      : [{ role: 'minimize' }, { role: 'close' }],
  }

  const helpMenu: MenuItemConstructorOptions = {
    label: 'Help',
    ...(mac ? { role: 'help' as const } : {}),
    submenu: [
      { label: 'Commander on GitHub', click: () => opts.dispatch({ type: 'open-external', url: REPO_URL }) },
      { label: 'Report an Issue', click: () => opts.dispatch({ type: 'open-external', url: `${REPO_URL}/issues` }) },
    ],
  }

  return [mac ? appMenu : fileMenu, editMenu, viewMenu, windowMenu, helpMenu]
}
