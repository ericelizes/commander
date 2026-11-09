import { describe, expect, it, vi } from 'vitest'
import { acceleratorFor, attachShortcuts, matchShortcut, type KeyInput, type ShortcutAction } from './shortcuts'

const mac = { platform: 'darwin', isDev: false }
const macDev = { platform: 'darwin', isDev: true }
const linux = { platform: 'linux', isDev: false }
const linuxDev = { platform: 'linux', isDev: true }

function key(code: string, mods: Partial<KeyInput> = {}): KeyInput {
  return { type: 'keyDown', key: '', code, meta: false, control: false, alt: false, shift: false, ...mods }
}

describe('matchShortcut', () => {
  it('maps Cmd+0..9 on macOS and Ctrl+0..9 on Linux to expand-or-go', () => {
    for (let n = 0; n <= 9; n++) {
      expect(matchShortcut(key(`Digit${n}`, { meta: true }), mac)).toEqual({ type: 'expand-or-go', n })
      expect(matchShortcut(key(`Digit${n}`, { control: true }), linux)).toEqual({ type: 'expand-or-go', n })
    }
  })

  it('also takes Ctrl+digit on macOS (TUI muscle memory) but not Super+digit on Linux', () => {
    expect(matchShortcut(key('Digit3', { control: true }), mac)).toEqual({ type: 'expand-or-go', n: 3 })
    expect(matchShortcut(key('Digit3', { meta: true }), linux)).toBeNull()
  })

  it('uses the platform modifier for the rest', () => {
    expect(matchShortcut(key('KeyK', { meta: true }), mac)).toEqual({ type: 'quick-switcher' })
    expect(matchShortcut(key('KeyK', { control: true }), mac)).toBeNull()
    expect(matchShortcut(key('KeyK', { control: true }), linux)).toEqual({ type: 'quick-switcher' })
    expect(matchShortcut(key('KeyK', { meta: true }), linux)).toBeNull()
    expect(matchShortcut(key('Comma', { meta: true }), mac)).toEqual({ type: 'settings' })
    expect(matchShortcut(key('Comma', { control: true }), linux)).toEqual({ type: 'settings' })
  })

  it('reads brackets by code, so Shift turning ] into } still matches', () => {
    expect(matchShortcut({ ...key('BracketRight', { meta: true, shift: true }), key: '}' }, mac)).toEqual({ type: 'next-workstream' })
    expect(matchShortcut({ ...key('BracketLeft', { meta: true, shift: true }), key: '{' }, mac)).toEqual({ type: 'prev-workstream' })
    expect(matchShortcut(key('BracketRight', { meta: true }), mac)).toBeNull()
  })

  it('reloads in every build, devtools only in dev, with per-platform chords', () => {
    expect(matchShortcut(key('KeyR', { meta: true }), mac)).toEqual({ type: 'reload' })
    expect(matchShortcut(key('KeyI', { meta: true, alt: true }), mac)).toBeNull()
    expect(matchShortcut(key('KeyR', { meta: true }), macDev)).toEqual({ type: 'reload' })
    expect(matchShortcut(key('KeyI', { meta: true, alt: true }), macDev)).toEqual({ type: 'devtools' })
    // Linux: Ctrl+R stays reverse-search; Ctrl+Shift+R reloads.
    expect(matchShortcut(key('KeyR', { control: true }), linuxDev)).toBeNull()
    expect(matchShortcut(key('KeyR', { control: true, shift: true }), linuxDev)).toEqual({ type: 'reload' })
    expect(matchShortcut(key('KeyI', { control: true, shift: true }), linuxDev)).toEqual({ type: 'devtools' })
  })

  it('ignores keyUp, plain keys, and extra modifiers', () => {
    expect(matchShortcut({ ...key('KeyK', { meta: true }), type: 'keyUp' }, mac)).toBeNull()
    expect(matchShortcut(key('KeyK'), mac)).toBeNull()
    expect(matchShortcut(key('Digit1', { meta: true, shift: true }), mac)).toBeNull()
    expect(matchShortcut(key('KeyK', { meta: true, alt: true }), mac)).toBeNull()
  })
})

// Parse an Electron accelerator into the key event it produces.
function inputFor(accel: string, platform: string): KeyInput {
  const mac = platform === 'darwin'
  const input = key('')
  for (const part of accel.split('+')) {
    if (part === 'CmdOrCtrl' && mac) input.meta = true
    else if (part === 'CmdOrCtrl') input.control = true
    else if (part === 'Cmd') input.meta = true
    else if (part === 'Ctrl') input.control = true
    else if (part === 'Alt') input.alt = true
    else if (part === 'Shift') input.shift = true
    else if (/^[0-9]$/.test(part)) input.code = `Digit${part}`
    else if (/^[A-Z]$/.test(part)) input.code = `Key${part}`
    else input.code = { ']': 'BracketRight', '[': 'BracketLeft', ',': 'Comma' }[part] ?? part
  }
  return input
}

describe('acceleratorFor', () => {
  const actions: ShortcutAction[] = [
    { type: 'expand-or-go', n: 0 },
    { type: 'expand-or-go', n: 7 },
    { type: 'quick-switcher' },
    { type: 'next-workstream' },
    { type: 'prev-workstream' },
    { type: 'settings' },
    { type: 'reload' },
    { type: 'devtools' },
  ]
  for (const platform of ['darwin', 'linux']) {
    it(`round-trips through matchShortcut on ${platform}`, () => {
      for (const action of actions) {
        const input = inputFor(acceleratorFor(action, platform), platform)
        expect(matchShortcut(input, { platform, isDev: true })).toEqual(action)
      }
    })
  }
})

describe('attachShortcuts', () => {
  it('prevents default and dispatches only matched keys', () => {
    let handler: (e: { preventDefault: () => void }, i: KeyInput) => void = () => {}
    const contents = { on: (_: string, fn: typeof handler) => { handler = fn } }
    const dispatch = vi.fn()
    attachShortcuts(contents as never, mac, dispatch)

    const hit = { preventDefault: vi.fn() }
    handler(hit, key('KeyK', { meta: true }))
    expect(hit.preventDefault).toHaveBeenCalled()
    expect(dispatch).toHaveBeenCalledWith({ type: 'quick-switcher' })

    const miss = { preventDefault: vi.fn() }
    handler(miss, key('KeyA', { meta: true }))
    expect(miss.preventDefault).not.toHaveBeenCalled()
    expect(dispatch).toHaveBeenCalledTimes(1)
  })
})
