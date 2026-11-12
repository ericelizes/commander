// Main-process wiring of the place runner (place-runner.ts): the sealed
// device key, the channel to cmdrd, the native dialogs, the run directories,
// the user's claude and the quit prompt. Started only on macOS and only once
// this Mac holds a device key (Pair this Mac); $COMMANDER_PLACE_RUNNER=off
// turns it off. Needs `cmdr app install` to take effect (main process).

import { execFile, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync, openSync, readSync, closeSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'
import type { App, Dialog, SafeStorage } from 'electron'
import {
  connectPlaceChannel,
  createPlaceRunner,
  loadDeviceKey,
  quitPrompt,
  runPaths,
  type KeySeal,
  type PlaceConfig,
  type SocketLike,
} from './place-runner'

export interface PlaceMainDeps {
  app: Pick<App, 'getPath' | 'getVersion' | 'on' | 'quit'>
  dialog: Pick<Dialog, 'showMessageBox'>
  safeStorage: Pick<SafeStorage, 'isEncryptionAvailable' | 'encryptString' | 'decryptString'>
  api: () => { apiUrl: string; token: string }
  commanderHome: string
  log: (msg: string) => void
  platform?: string
  env?: NodeJS.ProcessEnv
}

const KEY_FILE = 'place-device.key'

function keySeal(d: PlaceMainDeps): KeySeal {
  const path = join(d.app.getPath('userData'), KEY_FILE)
  return {
    read: () => (existsSync(path) ? readFileSync(path) : null),
    write: (b) => writeFileSync(path, b, { mode: 0o600 }),
    encrypt: (p) => {
      if (!d.safeStorage.isEncryptionAvailable()) throw new Error('the Keychain is not available to seal the device key')
      return d.safeStorage.encryptString(p)
    },
    decrypt: (b) => d.safeStorage.decryptString(b),
  }
}

/** which finds a binary the way the user's login shell would, in the usual places. */
function which(name: string, env: NodeJS.ProcessEnv): string {
  const dirs = [join(homedir(), '.local', 'bin'), '/opt/homebrew/bin', '/usr/local/bin', ...(env.PATH ?? '').split(':')]
  for (const d of dirs) {
    const p = join(d, name)
    if (d && existsSync(p)) return p
  }
  return ''
}

function execText(cmd: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) =>
    execFile(cmd, args, { timeout: 10_000, maxBuffer: 1 << 20 }, (err, out) => (err ? reject(err) : resolve(String(out)))),
  )
}

/** refusalsReader returns the guard's refusals logged since the last call. */
function refusalsReader(home: string): () => string[] {
  const path = join(home, 'logs', 'place-refusals.jsonl')
  let offset = existsSync(path) ? statSync(path).size : 0
  return () => {
    if (!existsSync(path)) return []
    const size = statSync(path).size
    if (size < offset) offset = 0
    if (size === offset) return []
    const buf = Buffer.alloc(Math.min(size - offset, 64 << 10))
    const fd = openSync(path, 'r')
    try {
      readSync(fd, buf, 0, buf.length, offset)
    } finally {
      closeSync(fd)
    }
    offset += buf.length
    return buf.toString('utf8').split('\n').filter(Boolean).map((l) => {
      try {
        const r = JSON.parse(l) as { tool?: string; command?: string; why?: string }
        return `${r.tool ?? ''} ${r.command ?? ''}: ${r.why ?? ''}`.trim()
      } catch {
        return l
      }
    })
  }
}

/** pairThisMac asks cmdrd for a pairing code for this Mac's key and shows it. */
export async function pairThisMac(d: PlaceMainDeps): Promise<void> {
  const key = loadDeviceKey(keySeal(d))
  const { apiUrl, token } = d.api()
  const res = await fetch(`${apiUrl}/api/places/pair`, { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ public_key: key.publicKey }) })
  const body = (await res.json().catch(() => ({}))) as { code?: string; error?: string }
  if (!body.code) {
    await d.dialog.showMessageBox({ type: 'error', message: 'Could not pair this Mac', detail: body.error ?? `cmdrd answered ${res.status}` })
    return
  }
  await d.dialog.showMessageBox({
    type: 'info',
    message: `Pairing code: ${body.code}`,
    detail: `In your own terminal on cmdrd's machine, run:\n\n  cmdr place pair ${body.code} --name <a short name for this Mac>\n\nThe code works once, for 10 minutes. Then restart the app to connect. Steps run here only when their Manager labels them run-on:mac:<name> under a closed user gate, and every run asks you first.`,
    buttons: ['OK'],
  })
}

/** startPlaces connects this Mac as a place, when it has been paired. */
export function startPlaces(d: PlaceMainDeps): { pair: () => void; stopAll: () => void } | null {
  const env = d.env ?? process.env
  const off = { pair: () => void pairThisMac(d).catch((e) => d.log(`place: pairing failed: ${(e as Error).message}`)), stopAll: () => {} }
  if ((d.platform ?? process.platform) !== 'darwin' || env.COMMANDER_PLACE_RUNNER === 'off') return null
  if (!existsSync(join(d.app.getPath('userData'), KEY_FILE))) return off
  const WS = (globalThis as { WebSocket?: new (url: string) => SocketLike }).WebSocket
  const cmdr = which('cmdr', env)
  const claude = which('claude', env)
  if (!WS || !cmdr || !claude) {
    d.log(`place: not started (WebSocket ${!!WS}, cmdr ${cmdr || 'missing'}, claude ${claude || 'missing'})`)
    return off
  }
  const { apiUrl, token } = d.api()
  const cfg: PlaceConfig = { apiUrl, cmdr, claude, runsDir: join(d.app.getPath('userData'), 'runs') }
  let channel: ReturnType<typeof connectPlaceChannel> | null = null
  const runner = createPlaceRunner({
    cfg,
    ask: (m) => channel?.ask(m) ?? Promise.resolve({ type: 'error', error: 'not connected' }),
    send: (m) => channel?.send(m),
    confirm: async (message, detail, timeoutMs) => {
      // The dialog cannot be dismissed by cmdrd; a timeout reads as Cancel.
      const timeout = new Promise<boolean>((r) => setTimeout(() => r(false), timeoutMs))
      const asked = d.dialog
        .showMessageBox({ type: 'warning', title: 'Run on this Mac', message, detail, buttons: ['Cancel', 'Run on this Mac'], defaultId: 0, cancelId: 0, noLink: true })
        .then((r) => r.response === 1)
      return Promise.race([asked, timeout])
    },
    post: async (runId, runToken, path, body) => {
      const res = await fetch(`${apiUrl}/api/places/runs/${runId}/${path}`, {
        method: 'POST',
        headers: { 'X-Commander-Run-Token': runToken, 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(20_000),
      })
      return res.status
    },
    prepare: (runId, settings) => {
      const p = runPaths(cfg.runsDir, runId)
      mkdirSync(p.runDir, { recursive: true, mode: 0o700 })
      mkdirSync(p.launchDir, { recursive: true, mode: 0o700 })
      writeFileSync(p.settingsPath, settings, { mode: 0o600 })
      writeFileSync(p.urlPath, cfg.apiUrl, { mode: 0o600 })
      return { runDir: p.runDir, settingsPath: p.settingsPath }
    },
    rolePrompt: () => execText(cmdr, ['place', 'prompt']),
    spawn: (l) => spawn(l.command, l.args, { cwd: l.cwd, env: l.env, detached: true, stdio: ['ignore', 'pipe', 'ignore'] }),
    killGroup: (pid) => terminatePlaceGroup(pid),
    refusals: refusalsReader(d.commanderHome),
    newSessionId: randomUUID,
    log: d.log,
  })
  void execText(cmdr, ['place', 'revision']).then((rev) => {
    channel = connectPlaceChannel(
      {
        url: apiUrl.replace(/^http/, 'ws') + '/api/places/connect?token=' + encodeURIComponent(token),
        open: (u) => new WS(u),
        key: loadDeviceKey(keySeal(d)),
        cmdrVersion: rev.trim(),
        appVersion: d.app.getVersion(),
        onWelcome: (place) => d.log(`place: connected as ${place}`),
        onRefused: (why) => d.log(`place: cmdrd refused this Mac: ${why}`),
        log: d.log,
      },
      (m) => runner.onMessage(m),
    )
  }, (e) => d.log(`place: cmdr place revision failed: ${(e as Error).message}`))

  installPlaceQuit(d.app, d.dialog, runner, () => channel?.close(), d.log)

  return { pair: off.pair, stopAll: () => runner.stopAll() }
}

/** Keep Electron alive until even a TERM-ignoring descendant has gone. */
export async function terminatePlaceGroup(pid: number, kill = process.kill, delay = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))): Promise<void> {
  const signal = (sig: NodeJS.Signals | 0): boolean => {
    try { kill(-pid, sig); return true } catch (e) {
      if ((e as NodeJS.ErrnoException).code === 'ESRCH') return false
      throw e
    }
  }
  if (!signal('SIGTERM')) return
  for (let n = 0; n < 50; n++) {
    if (!signal(0)) return
    await delay(100)
  }
  if (!signal('SIGKILL')) return
  for (let n = 0; n < 50; n++) {
    if (!signal(0)) return
    await delay(100)
  }
  throw new Error('place process group still exists after SIGKILL')
}

export function installPlaceQuit(app: PlaceMainDeps['app'], dialog: PlaceMainDeps['dialog'], runner: Pick<ReturnType<typeof createPlaceRunner>, 'active' | 'stopAll'>, close: () => void, log: PlaceMainDeps['log']) {
  let quitting = false
  let stopping = false
  app.on('before-quit', (ev) => {
    if (quitting) return
    const q = quitPrompt(runner.active())
    if (!q && !stopping) return
    ev.preventDefault()
    if (stopping) return
    stopping = true
    void (async () => {
      const r = await dialog.showMessageBox({ type: 'warning', message: q!.message, detail: q!.detail, buttons: ['Cancel', 'Stop and quit'], defaultId: 0, cancelId: 0 })
      if (r.response !== 1) return
      await runner.stopAll()
      close()
      quitting = true
      app.quit()
    })().catch((e) => log(`place: could not stop before quitting: ${(e as Error).message}`)).finally(() => { stopping = false })
  })
}
