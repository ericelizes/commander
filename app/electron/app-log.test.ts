import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { EventEmitter } from 'node:events'
import { describe, expect, it } from 'vitest'
import { appLogPath, consoleLevel, createAppLog, logProcess, logWindow } from './app-log'

function setup() {
  const lines: string[] = []
  const log = createAppLog('/x/app.log', (_p, s) => lines.push(s), () => new Date('2026-10-21T10:00:00Z'))
  const win = new EventEmitter()
  const wc = new EventEmitter()
  logWindow(win, wc, log)
  return { lines, win, wc }
}

describe('app log', () => {
  it('writes timestamped lines under <home>/logs/app.log, creating the dir', () => {
    const home = mkdtempSync(join(tmpdir(), 'applog-'))
    const path = appLogPath(home)
    expect(path).toBe(join(home, 'logs', 'app.log'))
    const log = createAppLog(path)
    log('error', 'boom\n')
    log('info', 'ok')
    expect(readFileSync(path, 'utf8')).toMatch(/^\S+Z ERROR boom\n\S+Z INFO ok\n$/)
  })

  it('never throws when the file cannot be written', () => {
    const log = createAppLog('/x/y', () => {
      throw new Error('EACCES')
    })
    expect(() => log('error', 'x')).not.toThrow()
  })

  it('records renderer console warnings/errors (both Electron signatures), not info', () => {
    const { lines, wc } = setup()
    wc.emit('console-message', { level: 'error', message: 'TypeError: x is undefined', sourceId: 'app.js', lineNumber: 3 })
    wc.emit('console-message', {}, 2, 'old-style warning', 9, 'a.js')
    wc.emit('console-message', { level: 'info', message: 'hello' })
    expect(lines).toEqual([
      '2026-10-21T10:00:00.000Z ERROR renderer console: TypeError: x is undefined (app.js:3)\n',
      '2026-10-21T10:00:00.000Z WARN renderer console: old-style warning (a.js:9)\n',
    ])
    expect(consoleLevel(1)).toBeUndefined()
  })

  it('records crashes, hangs and failed loads; a dead dev server is only info', () => {
    const { lines, win, wc } = setup()
    wc.emit('render-process-gone', {}, { reason: 'crashed', exitCode: 11 })
    win.emit('unresponsive')
    wc.emit('did-fail-load', {}, -102, 'ERR_CONNECTION_REFUSED', 'http://127.0.0.1:5173/app/', true)
    wc.emit('did-fail-load', {}, -6, 'ERR_FILE_NOT_FOUND', 'file:///dist/index.html', true)
    wc.emit('did-fail-load', {}, -3, 'ERR_ABORTED', 'file:///dist/index.html', true)
    expect(lines.map((l) => l.slice(25))).toEqual([
      'ERROR renderer gone: crashed (exit 11)\n',
      'WARN window unresponsive\n',
      'INFO load failed: http://127.0.0.1:5173/app/ ERR_CONNECTION_REFUSED (-102), showing the bundle\n',
      'ERROR load failed: file:///dist/index.html ERR_FILE_NOT_FOUND (-6)\n',
    ])
  })

  it('records main-process uncaught errors', () => {
    const lines: string[] = []
    const proc = new EventEmitter()
    logProcess(proc, createAppLog('/x', (_p, s) => lines.push(s)))
    proc.emit('unhandledRejection', new Error('nope'))
    expect(lines[0]).toContain('ERROR main unhandled rejection: Error: nope')
  })
})
