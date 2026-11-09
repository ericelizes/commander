import { legacyDaemonLabel } from './legacy-launchd'
// Make sure cmdrd (the API the app is a client of) is up, and keep watching
// it. On launch: read <COMMANDER_HOME>/api-url and probe it; if down, kick
// the service unit (launchctl on macOS, systemctl --user on Linux; on Linux
// only, a failed unit falls back to spawning cmdrd directly), and poll until
// it answers. A cmdrd that holds <COMMANDER_HOME>/cmdrd.lock but answers
// slowly (just woke from sleep, building its first snapshot) is waited on and
// never kicked: a second cmdrd exits at once anyway. Every state
// change goes to onState, which main forwards to the renderer over IPC
// ('daemon:state') so it can show "offline: starting cmdrd…".

import { spawn, execFile } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import * as os from 'node:os'
import * as path from 'node:path'

export type DaemonStatus = 'checking' | 'starting' | 'online' | 'offline'

export interface DaemonState {
  status: DaemonStatus
  url?: string
  message?: string
}

export const LAUNCHD_LABEL = 'org.commander'
export const SYSTEMD_UNIT = 'commander.service'

export interface DaemonDeps {
  platform: string
  commanderHome: string
  home: string
  uid: number
  readText: (file: string) => Promise<string>
  probe: (baseURL: string) => Promise<boolean>
  run: (cmd: string, args: string[]) => Promise<void>
  /** lockHolderAlive: the pid in cmdrd.lock is a running cmdrd daemon. */
  lockHolderAlive: () => Promise<boolean>
  spawnDetached: (cmd: string, args: string[], env: Record<string, string>) => void
  sleep: (ms: number) => Promise<void>
}

export interface EnsureOptions {
  timeoutMs?: number
  intervalMs?: number
  onState?: (state: DaemonState) => void
}

export async function readApiURL(deps: DaemonDeps): Promise<string | undefined> {
  try {
    const url = (await deps.readText(path.join(deps.commanderHome, 'api-url'))).trim().replace(/\/+$/, '')
    return url || undefined
  } catch {
    return undefined
  }
}

export async function readApiToken(deps: DaemonDeps): Promise<string | undefined> {
  try {
    return (await deps.readText(path.join(deps.commanderHome, 'api-token'))).trim() || undefined
  } catch {
    return undefined
  }
}

async function probeNow(deps: DaemonDeps): Promise<string | undefined> {
  const url = await readApiURL(deps)
  return url && (await deps.probe(url)) ? url : undefined
}

// daemonEnv is what the service unit gives cmdrd (install/launchd,
// install/systemd): a GUI app's own PATH is the bare system one, and a cmdrd
// started with it cannot find br, tmux or claude.
export function daemonEnv(home: string, currentPath = ''): Record<string, string> {
  const dirs = [
    path.join(home, '.local', 'bin'), path.join(home, '.cargo', 'bin'), '/opt/homebrew/bin', '/usr/local/bin',
    ...currentPath.split(':'), '/usr/bin', '/bin', '/usr/sbin', '/sbin',
  ]
  return { PATH: [...new Set(dirs.filter(Boolean))].join(':'), COMMANDER_CHAT_HANDOVER: '1' }
}

// kickDaemon asks the service manager to start cmdrd. On macOS that is all it
// does: a kickstart fails while install.sh has the job booted out, and a cmdrd
// we spawned then would hold :8787 in place of the launchd one (another PATH,
// another env, no log) and leave the job unloaded. We keep polling instead; the
// bootstrap starts it. Linux without a user unit gets a direct start.
export async function kickDaemon(deps: DaemonDeps): Promise<void> {
  if (deps.platform === 'darwin') {
    try {
      await deps.run('/bin/launchctl', ['kickstart', `gui/${deps.uid}/${LAUNCHD_LABEL}`])
    } catch {
      try {
 await deps.run('/bin/launchctl', ['kickstart', `gui/${deps.uid}/${legacyDaemonLabel}`])
 } catch { /* neither installed service is loaded yet */ }
    }
    return
  }
  try {
    if (deps.platform === 'linux') {
      await deps.run('systemctl', ['--user', 'start', SYSTEMD_UNIT])
      return
    }
  } catch {
    // fall through to a direct start
  }
  deps.spawnDetached(path.join(deps.home, '.local', 'bin', 'cmdrd'), [], daemonEnv(deps.home, process.env.PATH))
}

// ensureDaemon resolves with the final state: online (with its URL) or
// offline after timeoutMs. The service is kicked only while no live cmdrd
// holds the lock, so a slow daemon is never doubled and a dead one still is
// restarted.
export async function ensureDaemon(deps: DaemonDeps, opts: EnsureOptions = {}): Promise<DaemonState> {
  const { timeoutMs = 15000, intervalMs = 500, onState = () => {} } = opts
  onState({ status: 'checking' })
  let url = await probeNow(deps)
  if (url) {
    const state: DaemonState = { status: 'online', url }
    onState(state)
    return state
  }
  let kicked = false
  let busy = false
  const kickIfDead = async () => {
    if (kicked) return
    if (await deps.lockHolderAlive()) {
      if (!busy) onState({ status: 'starting', message: 'cmdrd is running but slow to answer; waiting…' })
      busy = true
      return
    }
    onState({ status: 'starting', message: 'offline: starting cmdrd…' })
    kicked = true
    await kickDaemon(deps)
  }
  await kickIfDead()
  for (let waited = 0; waited < timeoutMs; waited += intervalMs) {
    await deps.sleep(intervalMs)
    url = await probeNow(deps)
    if (url) {
      const state: DaemonState = { status: 'online', url }
      onState(state)
      return state
    }
    await kickIfDead()
  }
  let message = deps.platform === 'darwin'
    ? `cmdrd did not start; is launchd's ${LAUNCHD_LABEL} loaded? see ~/.commander/logs/cmdrd.err`
    : 'cmdrd did not start; see ~/.commander/logs/cmdrd.err'
  if (!kicked) message = 'cmdrd is running but not answering; see ~/.commander/logs/cmdrd.err'
  const state: DaemonState = { status: 'offline', message }
  onState(state)
  return state
}

// watchDaemon keeps probing; when cmdrd drops it re-runs ensureDaemon. A miss
// while a live cmdrd holds the lock (the first probes after a wake) backs off
// and re-checks instead. It returns a stop function.
export function watchDaemon(deps: DaemonDeps, onState: (s: DaemonState) => void, everyMs = 5000, maxBackoffMs = 30000): () => void {
  let stopped = false
  let last: DaemonStatus | undefined
  const emit = (s: DaemonState) => {
    last = s.status
    onState(s)
  }
  const loop = async () => {
    await ensureDaemon(deps, { onState: emit })
    let delay = everyMs
    while (!stopped) {
      await deps.sleep(delay)
      if (stopped) break
      const url = await probeNow(deps)
      if (url) {
        delay = everyMs
        if (last !== 'online') emit({ status: 'online', url })
        continue
      }
      if (await deps.lockHolderAlive()) {
        delay = Math.min(delay * 2, maxBackoffMs)
        continue
      }
      delay = everyMs
      await ensureDaemon(deps, { onState: emit })
    }
  }
  void loop()
  return () => {
    stopped = true
  }
}

// cmdrdDaemonArgs is true for `ps -o args=` of the cmdrd daemon itself, not a
// chat host or bridge (cmdrd __chat-host ...), matching cmd/cmdr isCmdrdDaemon.
export function cmdrdDaemonArgs(args: string): boolean {
  const [exe, ...rest] = args.trim().split(/\s+/)
  const base = path.basename(exe ?? '')
  return (base === 'cmdrd' || base === 'cmdrd.prev') && !rest.some((a) => a.startsWith('__'))
}

export function nodeDaemonDeps(commanderHome?: string): DaemonDeps {
  const home = os.homedir()
  const ch = commanderHome ?? process.env.COMMANDER_HOME ?? path.join(home, '.commander')
  return {
    platform: process.platform,
    commanderHome: ch,
    home,
    uid: process.getuid?.() ?? 0,
    readText: (file) => readFile(file, 'utf8'),
    probe: async (baseURL) => {
      try {
        // Generous: at load 30 a healthy cmdrd can take over a second, and a
        // miss counts as down and kicks the service.
        const res = await fetch(`${baseURL}/`, { signal: AbortSignal.timeout(3000) })
        return res.status < 500
      } catch {
        return false
      }
    },
    run: (cmd, args) =>
      new Promise((resolve, reject) => {
        execFile(cmd, args, { timeout: 5000 }, (err) => (err ? reject(err) : resolve()))
      }),
    lockHolderAlive: async () => {
      let pid: number
      try {
        pid = Number.parseInt((await readFile(path.join(ch, 'cmdrd.lock'), 'utf8')).trim(), 10)
      } catch {
        return false
      }
      if (!(pid > 0)) return false
      // The file keeps the last pid after its daemon exits; check it is still cmdrd.
      return new Promise((resolve) => {
        execFile('/bin/ps', ['-p', String(pid), '-o', 'args='], { timeout: 5000 }, (err, stdout) => {
          resolve(!err && cmdrdDaemonArgs(String(stdout)))
        })
      })
    },
    spawnDetached: (cmd, args, env) => {
      const child = spawn(cmd, args, { detached: true, stdio: 'ignore', env: { ...process.env, ...env } })
      child.on('error', () => {}) // a missing binary just leaves us offline
      child.unref()
    },
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  }
}
