// App-wide keyboard shortcuts, matched in main via `before-input-event` so
// they fire even when a terminal or webview inside the window has focus.
// Pure: key event in, action out. The modifier is Cmd on macOS, Ctrl
// elsewhere.

import type { WebContents } from 'electron'

export type ShortcutAction =
  | { type: 'expand-or-go', n: number }
  | { type: 'quick-switcher' }
  | { type: 'next-workstream' }
  | { type: 'prev-workstream' }
  | { type: 'settings' }
  | { type: 'reload' }
  | { type: 'devtools' }

// The subset of Electron's Input we read.
export interface KeyInput {
  type: string
  key: string
  code: string
  meta: boolean
  control: boolean
  alt: boolean
  shift: boolean
}

export interface ShortcutOptions {
  platform: string
  isDev: boolean
}

export function matchShortcut(input: KeyInput, opts: ShortcutOptions): ShortcutAction | null {
  if (input.type !== 'keyDown') return null
  const mac = opts.platform === 'darwin'
  const mod = mac ? input.meta && !input.control : input.control && !input.meta
  const { alt, shift, code } = input

  // Cmd+0..9 on macOS (Ctrl+0..9 too, the TUI's muscle memory); Ctrl+0..9 elsewhere.
  const digit = /^Digit([0-9])$/.exec(code)
  if (digit && !alt && !shift) {
    const ctrlOnly = input.control && !input.meta
    if (mod || (mac && ctrlOnly)) return { type: 'expand-or-go', n: Number(digit[1]) }
  }
  if (!mod) return null

  if (code === 'KeyK' && !alt && !shift) return { type: 'quick-switcher' }
  if (code === 'BracketRight' && shift && !alt) return { type: 'next-workstream' }
  if (code === 'BracketLeft' && shift && !alt) return { type: 'prev-workstream' }
  if (code === 'Comma' && !alt && !shift) return { type: 'settings' }

  // macOS: Cmd+R, Cmd+Alt+I. Elsewhere Ctrl+Shift+R / Ctrl+Shift+I, so
  // Ctrl+R stays reverse-search in a terminal. Reload works in every build:
  // it is how a window stuck on an error comes back.
  if (code === 'KeyR' && !alt && shift === !mac) return { type: 'reload' }
  if (opts.isDev) {
    if (code === 'KeyI' && (mac ? alt && !shift : shift && !alt)) return { type: 'devtools' }
  }
  return null
}

// Menu accelerator for an action, matching matchShortcut on that platform.
export function acceleratorFor(action: ShortcutAction, platform: string): string {
  const mac = platform === 'darwin'
  switch (action.type) {
    case 'expand-or-go': return `CmdOrCtrl+${action.n}`
    case 'quick-switcher': return 'CmdOrCtrl+K'
    case 'next-workstream': return 'CmdOrCtrl+Shift+]'
    case 'prev-workstream': return 'CmdOrCtrl+Shift+['
    case 'settings': return 'CmdOrCtrl+,'
    case 'reload': return mac ? 'Cmd+R' : 'Ctrl+Shift+R'
    case 'devtools': return mac ? 'Alt+Cmd+I' : 'Ctrl+Shift+I'
  }
}

// attachShortcuts routes matched keys to dispatch and keeps them from the
// page (and from menu accelerators, so nothing fires twice).
export function attachShortcuts(
  contents: WebContents,
  opts: ShortcutOptions,
  dispatch: (action: ShortcutAction) => void,
): void {
  contents.on('before-input-event', (event, input) => {
    const action = matchShortcut(input, opts)
    if (!action) return
    event.preventDefault()
    dispatch(action)
  })
}
