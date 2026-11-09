// Dock badge (NEEDS YOU count) and dock menu (first 5 workstreams).
// macOS only for the dock; Linux gets the launcher badge where the desktop
// supports it (Unity/KDE via app.setBadgeCount) and no dock menu.

import type { App, MenuItemConstructorOptions } from 'electron'
import type { DockWorkstream } from './shell-channels'

export const DOCK_MENU_LIMIT = 5

export function badgeText(count: number): string {
  if (!Number.isFinite(count) || count <= 0) return ''
  return count > 99 ? '99+' : String(Math.floor(count))
}

export function setNeedsYouBadge(app: App, platform: string, count: number): void {
  if (platform === 'darwin') {
    app.dock?.setBadge(badgeText(count))
  } else if (platform === 'linux') {
    app.setBadgeCount(Math.max(0, Math.floor(count) || 0))
  }
}

export function dockMenuTemplate(
  workstreams: DockWorkstream[],
  open: (id: string) => void,
): MenuItemConstructorOptions[] {
  return workstreams.slice(0, DOCK_MENU_LIMIT).map((ws) => ({
    label: ws.name,
    click: () => open(ws.id),
  }))
}
