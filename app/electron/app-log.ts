// ~/.commander/logs/app.log: what went wrong in the app, for "the app broke"
// reports. Main-process errors plus the renderer's console warnings/errors,
// crashes, hangs and failed loads. Append-only, one line per entry; logging
// never throws.

import { appendFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'

export type LogLevel = 'info' | 'warn' | 'error'
export type AppLog = (level: LogLevel, msg: string) => void

export function appLogPath(commanderHome: string): string {
  return join(commanderHome, 'logs', 'app.log')
}

const writeLine = (p: string, s: string) => {
  mkdirSync(dirname(p), { recursive: true })
  appendFileSync(p, s)
}

export function createAppLog(path: string, append: (p: string, s: string) => void = writeLine, now: () => Date = () => new Date()): AppLog {
  return (level, msg) => {
    try {
      append(path, `${now().toISOString()} ${level.toUpperCase()} ${msg.replace(/\s+$/, '')}\n`)
    } catch {
      // a log that cannot be written must not break the app
    }
  }
}

/** consoleLevel maps Electron's console-message level (number before Electron 35, string after) to ours; undefined = not logged. */
export function consoleLevel(level: unknown): LogLevel | undefined {
  if (level === 2 || level === 'warning') return 'warn'
  if (level === 3 || level === 'error') return 'error'
  return undefined
}

interface Emitter {
  on(event: string, listener: (...args: any[]) => void): unknown // eslint-disable-line @typescript-eslint/no-explicit-any
}

/** logWindow records a window's renderer trouble: console warn/error, crashes, hangs, failed loads. */
export function logWindow(win: Emitter, wc: Emitter, log: AppLog): void {
  wc.on('console-message', (...args: unknown[]) => {
    // Electron >= 35: (event with .level/.message/.sourceId/.lineNumber); older: (event, level, message, line, sourceId)
    const e = args[0] as { level?: unknown; message?: string; sourceId?: string; lineNumber?: number }
    const [level, message, line, source] = e && e.message !== undefined ? [e.level, e.message, e.lineNumber, e.sourceId] : [args[1], args[2], args[3], args[4]]
    const l = consoleLevel(level)
    if (l) log(l, `renderer console: ${String(message)}${source ? ` (${source}:${line ?? '?'})` : ''}`)
  })
  wc.on('render-process-gone', (_e: unknown, d: { reason?: string; exitCode?: number }) => log('error', `renderer gone: ${d?.reason ?? 'unknown'} (exit ${d?.exitCode ?? '?'})`))
  wc.on('did-fail-load', (_e: unknown, code: number, desc: string, url: string, isMainFrame: boolean) => {
    if (code === -3 || !isMainFrame) return // aborted by a newer load
    // The live dev server being down is expected: the bundle takes over silently.
    const dev = /^https?:\/\/(127\.0\.0\.1|localhost):\d+\/app\//.test(url)
    log(dev ? 'info' : 'error', `load failed: ${url} ${desc} (${code})${dev ? ', showing the bundle' : ''}`)
  })
  win.on('unresponsive', () => log('warn', 'window unresponsive'))
  win.on('responsive', () => log('info', 'window responsive again'))
}

/** logProcess records main-process crashes. */
export function logProcess(proc: Emitter, log: AppLog): void {
  proc.on('uncaughtException', (err: unknown) => log('error', `main uncaught: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}`))
  proc.on('unhandledRejection', (err: unknown) => log('error', `main unhandled rejection: ${err instanceof Error ? (err.stack ?? err.message) : String(err)}`))
}
