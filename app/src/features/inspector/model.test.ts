import { describe, expect, it } from 'vitest'

import { parseAnsi } from './ansi'
import {
  addTabArgv,
  ago,
  bodyMode,
  cardPollMs,
  focusTabArgv,
  hasDigest,
  neighborAfterClose,
  normalizeUrl,
  resolveActive,
  rmTabArgv,
  stateDot,
  syncMsg,
  tabIcon,
} from './model'
import { bodyRect, sameRect } from './rect'
import type { Card, TabInfo } from './types'

const tab = (id: number, over: Partial<TabInfo> = {}): TabInfo => ({
  id,
  position: id,
  title: `t${id}`,
  kind: 'url',
  target: `https://example.com/${id}`,
  renderer: '',
  detected: 'web',
  view: 'web',
  refresh_secs: 60,
  added_by: 'user',
  created_at: 0,
  ...over,
})

describe('tab commands', () => {
  it('targets the window', () => {
    expect(addTabArgv({}, 'https://a.com')).toEqual(['tab', 'add', '--url', 'https://a.com', '--focus'])
    expect(addTabArgv({ ws: 'bd-1', project: 'p' }, 'https://a.com', 'Sign in: Notion')).toEqual([
      'tab', 'add', '--url', 'https://a.com', '--title', 'Sign in: Notion', '--focus', '--ws', 'bd-1', '--project', 'p',
    ])
    expect(rmTabArgv({ ws: 'bd-1' }, 4)).toEqual(['tab', 'rm', '#4', '--ws', 'bd-1'])
    expect(focusTabArgv({ project: 'p' }, 4)).toEqual(['tab', 'focus', '#4']) // project alone is not a window
  })
})

describe('normalizeUrl', () => {
  it.each([
    ['linear.app/acme/issue/AB-1', 'https://linear.app/acme/issue/AB-1'],
    ['  https://github.com/o/r/pull/4 ', 'https://github.com/o/r/pull/4'],
    ['localhost:3000/x', 'http://localhost:3000/x'],
    ['http://127.0.0.1:8080', 'http://127.0.0.1:8080/'],
    ['hello world', null],
    ['javascript:alert(1)', null],
    ['ftp://x.com', null],
    ['foo', null],
    ['', null],
  ])('%s', (input, want) => {
    expect(normalizeUrl(input)).toBe(want)
  })
})

describe('body mode', () => {
  it('uses webviews only when the Electron bridge exists', () => {
    expect(bodyMode(tab(1), true)).toBe('webview')
    expect(bodyMode(tab(1), false)).toBe('link')
    expect(bodyMode(tab(1, { view: 'card', kind: 'cmd' }), false)).toBe('card')
    expect(bodyMode(tab(1, { view: 'decision', kind: 'decision' }), true)).toBe('decision')
  })
  it('knows which web tabs have a digest card', () => {
    expect(hasDigest(tab(1, { detected: 'notion' }))).toBe(true)
    expect(hasDigest(tab(1, { detected: 'github-pr' }))).toBe(true)
    expect(hasDigest(tab(1, { detected: 'web' }))).toBe(false)
    expect(hasDigest(tab(1, { detected: 'link' }))).toBe(false)
  })
})

describe('active tab', () => {
  const tabs = [tab(1), tab(2), tab(3)]
  it('prefers a pending click, then the server, then the first tab', () => {
    expect(resolveActive(tabs, 2, 3)).toBe(3)
    expect(resolveActive(tabs, 2, 9)).toBe(2) // pending tab vanished
    expect(resolveActive(tabs, 0, null)).toBe(1)
    expect(resolveActive([], 0, null)).toBeNull()
  })
  it('moves to a neighbour on close', () => {
    expect(neighborAfterClose(tabs, 2)).toBe(3)
    expect(neighborAfterClose(tabs, 3)).toBe(2)
    expect(neighborAfterClose([tab(1)], 1)).toBeNull()
  })
})

describe('syncMsg', () => {
  const rect = { x: 1, y: 2, width: 3, height: 4 }
  const tabs = [tab(1), tab(2, { view: 'card', kind: 'cmd', target: 'ls' })]
  it('lists only web tabs and shows the active one', () => {
    expect(syncMsg(tabs, 1, rect, true, false)).toEqual({ tabs: [{ id: 1, url: 'https://example.com/1' }], activeId: 1, rect })
  })
  it('hides views for card tabs and while a menu is open', () => {
    expect(syncMsg(tabs, 2, rect, true, false)).toEqual({ tabs: [{ id: 1, url: 'https://example.com/1' }], activeId: null, rect: null })
    expect(syncMsg(tabs, 1, rect, true, true).rect).toBeNull()
  })
  it('sends nothing in web mode', () => {
    expect(syncMsg(tabs, 1, rect, false, false)).toEqual({ tabs: [], activeId: null, rect: null })
  })
})

describe('icons and dots', () => {
  it('picks an icon by kind and site', () => {
    expect(tabIcon(tab(1, { kind: 'cmd', target: 'ls', view: 'card' })).site).toBe('cmd')
    expect(tabIcon(tab(1, { target: 'https://www.notion.so/x' })).site).toBe('notion')
    expect(tabIcon(tab(1, { target: 'https://github.com/o/r/pull/1' })).site).toBe('github')
    expect(tabIcon(tab(1, { target: 'https://app.datadoghq.com/' })).site).toBe('datadog')
    expect(tabIcon(tab(1, { kind: 'decision', view: 'decision', target: 'b-1' })).label).toBe('⚑')
  })
  it('derives the state dot', () => {
    const v = { id: 1, url: '', title: '', loading: false, error: null, canGoBack: false, canGoForward: false }
    expect(stateDot(tab(1))).toBe('none')
    expect(stateDot(tab(1), { ...v, loading: true })).toBe('loading')
    expect(stateDot(tab(1), { ...v, error: 'x' })).toBe('error')
    expect(stateDot(tab(1), v)).toBe('ok')
    const card = { Err: '', Attn: true } as Card
    expect(stateDot(tab(2, { view: 'card' }), undefined, { card, refreshing: false })).toBe('attn')
    expect(stateDot(tab(2, { view: 'card' }), undefined, { card: { ...card, Attn: false, Err: 'boom' }, refreshing: false })).toBe('error')
  })
})

describe('rect math', () => {
  it('snaps inward and drops empty rects', () => {
    expect(bodyRect({ left: 10.5, top: 20.2, width: 300, height: 200 })).toEqual({ x: 11, y: 21, width: 299, height: 199 })
    expect(bodyRect({ left: 0, top: 0, width: 100, height: 50 }, 1)).toEqual({ x: 1, y: 1, width: 98, height: 48 })
    expect(bodyRect({ left: 0, top: 0, width: 0.5, height: 50 })).toBeNull()
    expect(sameRect(null, null)).toBe(true)
    expect(sameRect({ x: 1, y: 1, width: 1, height: 1 }, { x: 1, y: 1, width: 1, height: 1 })).toBe(true)
    expect(sameRect({ x: 1, y: 1, width: 1, height: 1 }, null)).toBe(false)
  })
})

describe('misc', () => {
  it('formats ages and poll intervals', () => {
    const now = Date.parse('2026-10-20T12:00:00Z')
    expect(ago('2026-10-20T11:59:30Z', now)).toBe('just now')
    expect(ago('2026-10-20T11:00:00Z', now)).toBe('1h ago')
    expect(ago('0001-01-01T00:00:00Z', now)).toBe('')
    expect(cardPollMs(tab(1, { refresh_secs: 10 }), false)).toBe(10_000)
    expect(cardPollMs(tab(1, { refresh_secs: 300 }), false)).toBe(30_000)
    expect(cardPollMs(tab(1), true)).toBe(1500)
  })
  it('parses ANSI colors', () => {
    expect(parseAnsi('\x1b[1;32mok\x1b[0m done\r')).toEqual([{ text: 'ok', fg: '#a6e3a1', bold: true }, { text: ' done' }])
    expect(parseAnsi('\x1b[38;5;196mred\x1b[39m')).toEqual([{ text: 'red', fg: 'rgb(255,0,0)' }])
    expect(parseAnsi('\x1b]8;;http://x\x07link\x1b]8;;\x07\x1b[2K')).toEqual([{ text: 'link' }])
  })
})
