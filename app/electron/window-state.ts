// Remember the main window's bounds and maximized state across launches,
// in <userData>/window-state.json.

import * as fs from 'node:fs'
import * as path from 'node:path'
import type { BrowserWindow, Rectangle } from 'electron'

export interface WindowState {
  x?: number
  y?: number
  width: number
  height: number
  maximized: boolean
}

export const DEFAULT_WINDOW_STATE: WindowState = { width: 1440, height: 900, maximized: false }
const MIN_WIDTH = 800
const MIN_HEIGHT = 500

export function windowStateFile(userData: string): string {
  return path.join(userData, 'window-state.json')
}

// parseWindowState validates saved JSON against the current displays' work
// areas: bad data gives the default; a position off every display (a
// monitor was unplugged) is dropped so the window centers.
export function parseWindowState(raw: string | undefined, workAreas: Rectangle[]): WindowState {
  let saved: Partial<WindowState>
  try {
    saved = raw ? JSON.parse(raw) : {}
  } catch {
    return { ...DEFAULT_WINDOW_STATE }
  }
  const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined)
  const width = Math.max(MIN_WIDTH, num(saved.width) ?? DEFAULT_WINDOW_STATE.width)
  const height = Math.max(MIN_HEIGHT, num(saved.height) ?? DEFAULT_WINDOW_STATE.height)
  const state: WindowState = { width, height, maximized: saved.maximized === true }
  const x = num(saved.x)
  const y = num(saved.y)
  if (x !== undefined && y !== undefined && workAreas.some((a) => visibleOn({ x, y, width, height }, a))) {
    state.x = x
    state.y = y
  }
  return state
}

// visibleOn: at least a 100x50 corner of the title bar region is on screen.
function visibleOn(b: Rectangle, a: Rectangle): boolean {
  const left = Math.max(b.x, a.x)
  const right = Math.min(b.x + b.width, a.x + a.width)
  const top = Math.max(b.y, a.y)
  const bottom = Math.min(b.y + b.height, a.y + a.height)
  return right - left >= 100 && bottom - top >= 50
}

export function loadWindowState(file: string, workAreas: Rectangle[]): WindowState {
  let raw: string | undefined
  try {
    raw = fs.readFileSync(file, 'utf8')
  } catch {
    raw = undefined
  }
  return parseWindowState(raw, workAreas)
}

export function captureWindowState(win: BrowserWindow): WindowState {
  const b = win.getNormalBounds() // the unmaximized bounds, so restore works
  return { x: b.x, y: b.y, width: b.width, height: b.height, maximized: win.isMaximized() }
}

// trackWindowState saves on move/resize (debounced) and on close.
export function trackWindowState(win: BrowserWindow, file: string, debounceMs = 400): void {
  let timer: ReturnType<typeof setTimeout> | undefined
  const save = () => {
    if (win.isDestroyed()) return
    try {
      fs.mkdirSync(path.dirname(file), { recursive: true })
      fs.writeFileSync(file, JSON.stringify(captureWindowState(win)))
    } catch {
      // Losing the window position is not worth crashing over.
    }
  }
  const later = () => {
    clearTimeout(timer)
    timer = setTimeout(save, debounceMs)
  }
  win.on('resize', later)
  win.on('move', later)
  win.on('maximize', later)
  win.on('unmaximize', later)
  win.on('close', () => {
    clearTimeout(timer)
    save()
  })
}
