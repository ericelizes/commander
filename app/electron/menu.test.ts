import { describe, expect, it, vi } from 'vitest'
import type { MenuItemConstructorOptions } from 'electron'
import { buildMenuTemplate, REPO_URL } from './menu'

function build(platform: string, isDev = false) {
  const dispatch = vi.fn()
  const template = buildMenuTemplate({ platform, isDev, appName: 'Commander', dispatch })
  return { template, dispatch }
}

function flatten(items: MenuItemConstructorOptions[]): MenuItemConstructorOptions[] {
  return items.flatMap((i) => [i, ...(Array.isArray(i.submenu) ? flatten(i.submenu) : [])])
}

const roles = (items: MenuItemConstructorOptions[]) => flatten(items).map((i) => i.role).filter(Boolean)
const find = (items: MenuItemConstructorOptions[], label: string) => flatten(items).find((i) => i.label === label)

describe('buildMenuTemplate', () => {
  it('macOS: App, Edit, View, Window, Help with the mac roles', () => {
    const { template } = build('darwin')
    expect(template.map((m) => m.label)).toEqual(['Commander', 'Edit', 'View', 'Window', 'Help'])
    const r = roles(template)
    for (const role of ['about', 'services', 'hide', 'hideOthers', 'unhide', 'quit', 'front', 'zoom', 'pasteAndMatchStyle', 'help']) {
      expect(r).toContain(role)
    }
  })

  it('Linux: File menu instead of App menu, no macOS-only roles', () => {
    const { template } = build('linux')
    expect(template.map((m) => m.label)).toEqual(['File', 'Edit', 'View', 'Window', 'Help'])
    const r = roles(template)
    for (const role of ['about', 'services', 'hide', 'hideOthers', 'unhide', 'front', 'zoom', 'pasteAndMatchStyle', 'help']) {
      expect(r).not.toContain(role)
    }
    expect(r).toContain('quit')
    expect(find(template, 'Settings')?.accelerator).toBe('CmdOrCtrl+,')
  })

  it('shortcut items show accelerators but leave the keys to before-input-event', () => {
    const { template, dispatch } = build('darwin')
    const qs = find(template, 'Quick Switcher…')!
    expect(qs.accelerator).toBe('CmdOrCtrl+K')
    expect(qs.registerAccelerator).toBe(false)
    ;(qs.click as () => void)()
    expect(dispatch).toHaveBeenCalledWith({ type: 'quick-switcher' })

    const ws3 = find(template, 'Workstream 3')!
    expect(ws3.accelerator).toBe('CmdOrCtrl+3')
    ;(ws3.click as () => void)()
    expect(dispatch).toHaveBeenCalledWith({ type: 'expand-or-go', n: 3 })
    expect(find(template, 'Commander')).toBeDefined()
  })

  it('Reload in every build, devtools only in dev, with per-platform accelerators', () => {
    expect(find(build('darwin').template, 'Reload')?.accelerator).toBe('Cmd+R')
    expect(find(build('darwin').template, 'Toggle Developer Tools')).toBeUndefined()
    expect(find(build('darwin', true).template, 'Reload')?.accelerator).toBe('Cmd+R')
    expect(find(build('linux', true).template, 'Reload')?.accelerator).toBe('Ctrl+Shift+R')
    expect(find(build('linux', true).template, 'Toggle Developer Tools')?.accelerator).toBe('Ctrl+Shift+I')
  })

  it('Help opens the repo', () => {
    const { template, dispatch } = build('linux')
    ;(find(template, 'Commander on GitHub')!.click as () => void)()
    expect(dispatch).toHaveBeenCalledWith({ type: 'open-external', url: REPO_URL })
  })

  it('View names where the page comes from, once known', () => {
    const dispatch = vi.fn()
    const live = buildMenuTemplate({ platform: 'darwin', isDev: false, appName: 'Commander', dispatch, rendererSource: 'live' })
    expect(find(live, 'Renderer: Live (dev server)')?.enabled).toBe(false)
    const bundled = buildMenuTemplate({ platform: 'linux', isDev: false, appName: 'Commander', dispatch, rendererSource: 'bundled' })
    expect(find(bundled, 'Renderer: Bundled')).toBeDefined()
    expect(flatten(build('darwin').template).some((i) => i.label?.startsWith('Renderer:'))).toBe(false)
  })
})
