// Install from the desktop app when cmdrd cannot ask at its own screen: cmdrd
// on a headless box (no display) or on another machine than the app. The app
// shows the native confirmation HERE (main process, not the page) and, on
// Install, runs the install itself over SSH. Agents on cmdrd's machine cannot
// reach this dialog, so the person's click stays the protection.
//
// The renderer gets exactly two verbs (install, rollback) through the preload;
// everything the command is built from comes from main's own reads: cmdrd's
// GET /api/update (repo, host, main) and the app's SSH settings:
//   $COMMANDER_REMOTE_SSH or $COMMANDER_HOME/remote-ssh             an SSH target (host, user@host or an ~/.ssh/config alias)
//   $COMMANDER_REMOTE_SSH_SUFFIX or $COMMANDER_HOME/remote-ssh-suffix  else <cmdrd host><suffix>, e.g. ".example.net"

import { spawn } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import type { IpcMain, WebContents } from 'electron'
import * as ch from './shell-channels'

export type UpdateAction = 'install' | 'rollback'

/** What main reads from cmdrd's GET /api/update. */
export interface RemoteUpdateStatus {
  host?: string
  repo?: string
  main: string
  installed: string
  behind: number
  dirty: boolean
  can_rollback: boolean
  error?: string
  commits: { hash: string; subject: string; review: string }[]
}

export interface RemoteSettings {
  ssh?: string
  sshSuffix?: string
}

export interface RemoteUpdateResult {
  ok: boolean
  declined?: boolean
  error?: string
  output?: string[]
}

function readTrimmed(path: string): string {
  try {
    return readFileSync(path, 'utf8').trim()
  } catch {
    return ''
  }
}

export function remoteSettings(home: string, env: NodeJS.ProcessEnv = process.env): RemoteSettings {
  return {
    ssh: env.COMMANDER_REMOTE_SSH || readTrimmed(join(home, 'remote-ssh')) || undefined,
    sshSuffix: env.COMMANDER_REMOTE_SSH_SUFFIX || readTrimmed(join(home, 'remote-ssh-suffix')) || undefined,
  }
}

// No leading '-' (ssh would read it as an option) and nothing a shell or ssh
// treats specially.
const TARGET_RE = /^[A-Za-z0-9_][A-Za-z0-9._-]*(@[A-Za-z0-9_][A-Za-z0-9._-]*)?$/
const HOST_RE = /^[A-Za-z0-9][A-Za-z0-9.-]*$/
const SUFFIX_RE = /^\.[A-Za-z0-9][A-Za-z0-9.-]*$/

/** sshTarget: the explicit setting, else cmdrd's host plus the configured suffix. */
export function sshTarget(s: RemoteSettings, cmdrdHost: string | undefined): { target: string } | { error: string } {
  if (s.ssh) return TARGET_RE.test(s.ssh) ? { target: s.ssh } : { error: `the SSH target in remote-ssh is not a plain host name: ${s.ssh}` }
  if (s.sshSuffix) {
    if (!SUFFIX_RE.test(s.sshSuffix)) return { error: `remote-ssh-suffix must look like .example.net: ${s.sshSuffix}` }
    if (!cmdrdHost || !HOST_RE.test(cmdrdHost)) return { error: `cmdrd reported no usable host name (${cmdrdHost ?? 'none'})` }
    return { target: cmdrdHost + s.sshSuffix }
  }
  return { error: 'no SSH target for cmdrd: put the host in $COMMANDER_HOME/remote-ssh on this computer (or a suffix such as .example.net in remote-ssh-suffix)' }
}

export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

/**
 * remoteCommand is the shell line run on cmdrd's machine. An install first
 * checks the checkout is still on main at the confirmed commit with no
 * tracked edits, so it builds what the dialog named.
 */
export function remoteCommand(action: UpdateAction, repo: string | undefined, main: string): { command: string } | { error: string } {
  if (!repo || !repo.startsWith('/') || /[\p{Cc}]/u.test(repo)) return { error: `cmdrd did not report a usable checkout (${repo ?? 'none'})` }
  const env = 'export PATH=$HOME/.local/bin:$PATH XDG_RUNTIME_DIR=/run/user/$(id -u)'
  if (action === 'rollback') return { command: `${env}; cd ${shQuote(repo)} && cmdr install --rollback --yes --restart-cmdrd` }
  if (!/^[0-9a-f]{40}$/.test(main)) return { error: `cmdrd reported no full main revision (${main})` }
  const pin =
    `test "$(git symbolic-ref --short HEAD)" = main && test "$(git rev-parse HEAD)" = ${main} && test -z "$(git status --porcelain --untracked-files=no)"` +
    ` || { echo "main moved, the checkout is not on main, or it has uncommitted changes; nothing installed, try again" >&2; exit 1; }`
  return { command: `${env}; cd ${shQuote(repo)} && { ${pin}; } && just build && cmdr install --yes --restart-cmdrd` }
}

export function sshArgs(target: string, command: string): string[] {
  return ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '--', target, command]
}

// Commit subjects are untrusted text: no control, format or separator runes.
function clean(s: string): string {
  const t = s.replace(/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, ' ').replace(/\s+/g, ' ').trim()
  return t.length > 100 ? t.slice(0, 100) + '...' : t
}

/** confirmText is what the native dialog says: what installs, where, and how. */
export function confirmText(action: UpdateAction, st: RemoteUpdateStatus, target: string): { message: string; detail: string } {
  const host = st.host || target
  if (action === 'rollback') {
    return {
      message: `Roll back Commander on ${host}?`,
      detail: `Puts the previous cmdr and cmdrd back in place of ${st.installed.slice(0, 7) || 'the installed build'} and restarts cmdrd. Running chats keep running.\n\nRuns over SSH (${target}): cmdr install --rollback --yes --restart-cmdrd`,
    }
  }
  const lines = st.commits.slice(0, 10).map((c) => `${c.hash.slice(0, 7)} ${clean(c.subject)} (${c.review === 'reviewed' ? 'reviewed' : 'not confirmed'})`)
  const hidden = st.behind - lines.length
  return {
    message: `Install ${st.main.slice(0, 7)} on ${host}?`,
    detail:
      `main is ${st.behind} ${st.behind === 1 ? 'commit' : 'commits'} ahead of what runs:\n${lines.join('\n')}${hidden > 0 ? `\n...and ${hidden} more` : ''}\n\n` +
      `Builds and installs it, then restarts cmdrd. Running chats keep running.\n\nRuns over SSH (${target}) in ${st.repo}: just build && cmdr install --yes --restart-cmdrd`,
  }
}

export interface RemoteUpdateDeps {
  fetchStatus(): Promise<RemoteUpdateStatus>
  settings(): RemoteSettings
  confirm(wc: WebContents | undefined, message: string, detail: string): Promise<boolean>
  run(args: string[], onLine: (line: string) => void): Promise<{ code: number | null; error?: string }>
  now?: () => number
}

const PROMPT_EVERY_MS = 60_000
const PROMPTS_PER_HOUR = 5
const KEEP_LINES = 40

/**
 * createRemoteUpdater: one install at a time, and a floor on dialogs (one per
 * minute, five per hour) so a page calling the bridge in a loop cannot flood
 * the screen.
 */
export function createRemoteUpdater(deps: RemoteUpdateDeps) {
  const now = deps.now ?? Date.now
  let busy = false
  let prompts: number[] = []
  return async (action: unknown, onLine: (line: string) => void, wc?: WebContents): Promise<RemoteUpdateResult> => {
    if (action !== 'install' && action !== 'rollback') return { ok: false, error: 'action must be install or rollback' }
    if (busy) return { ok: false, error: 'an install is already running' }
    const t = now()
    prompts = prompts.filter((p) => t - p < 3_600_000)
    if (prompts.some((p) => t - p < PROMPT_EVERY_MS)) return { ok: false, error: 'a confirmation was just shown; try again in a minute' }
    if (prompts.length >= PROMPTS_PER_HOUR) return { ok: false, error: 'too many confirmations this hour' }
    busy = true
    try {
      let st: RemoteUpdateStatus
      try {
        st = await deps.fetchStatus()
      } catch (e) {
        return { ok: false, error: `cmdrd is not reachable: ${(e as Error).message}` }
      }
      if (st.error) return { ok: false, error: st.error }
      if (action === 'install' && st.behind <= 0) return { ok: false, error: 'already up to date' }
      if (action === 'install' && st.dirty) return { ok: false, error: 'the main checkout has uncommitted changes; commit or stash them first' }
      if (action === 'rollback' && !st.can_rollback) return { ok: false, error: 'no previous build to roll back to' }
      const target = sshTarget(deps.settings(), st.host)
      if ('error' in target) return { ok: false, error: target.error }
      const cmd = remoteCommand(action, st.repo, st.main)
      if ('error' in cmd) return { ok: false, error: cmd.error }
      const text = confirmText(action, st, target.target)
      prompts.push(now())
      if (!(await deps.confirm(wc, text.message, text.detail))) return { ok: false, declined: true }
      const tail: string[] = []
      const res = await deps.run(sshArgs(target.target, cmd.command), (line) => {
        tail.push(line)
        if (tail.length > KEEP_LINES) tail.shift()
        onLine(line)
      })
      if (res.code === 0) return { ok: true, output: tail }
      return { ok: false, error: res.error ?? `the install over SSH exited with code ${res.code}`, output: tail }
    } finally {
      busy = false
    }
  }
}

const RUN_TIMEOUT_MS = 20 * 60_000

/** runSsh runs /usr/bin/ssh with args (no local shell) and streams its output by line. */
export function runSsh(args: string[], onLine: (line: string) => void, spawnFn: typeof spawn = spawn): Promise<{ code: number | null; error?: string }> {
  return new Promise((resolve) => {
    const child = spawnFn('/usr/bin/ssh', args, { stdio: ['ignore', 'pipe', 'pipe'] })
    const timer = setTimeout(() => child.kill('SIGTERM'), RUN_TIMEOUT_MS)
    const split = () => {
      let rest = ''
      return (chunk: Buffer) => {
        const parts = (rest + chunk.toString('utf8')).split(/\r?\n/)
        rest = parts.pop() ?? ''
        for (const p of parts) if (p.trim()) onLine(p)
      }
    }
    child.stdout?.on('data', split())
    child.stderr?.on('data', split())
    child.on('error', (e) => {
      clearTimeout(timer)
      resolve({ code: null, error: `could not run ssh: ${e.message}` })
    })
    child.on('close', (code, signal) => {
      clearTimeout(timer)
      resolve(signal ? { code: null, error: `the install over SSH was stopped (${signal})` } : { code })
    })
  })
}

export function registerRemoteUpdateIpc(ipcMain: Pick<IpcMain, 'handle'>, deps: RemoteUpdateDeps): void {
  const update = createRemoteUpdater(deps)
  ipcMain.handle(ch.UPDATE_REMOTE_RUN, (e, action: unknown) =>
    update(action, (line) => {
      if (!e.sender.isDestroyed()) e.sender.send(ch.UPDATE_REMOTE_LINE, line)
    }, e.sender),
  )
}
