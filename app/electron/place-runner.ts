// A governed Operator on this Mac (a paired place). The app's MAIN process
// holds cmdrd's place channel; cmdrd only names runs (ids and a brief hash),
// asks for a stop, or signals mail. Everything that runs is decided here:
//
//   - every run opens a native dialog first (Cancel is the default, 10 min = Cancel);
//   - the brief's sha256 must match the offer, so what the dialog showed is what runs;
//   - the command line is a fixed template (buildLaunch): the user's own claude,
//     no edit tools, no MCP servers, the local read-only guard and mail hook on
//     every tool call, cwd = a private run directory;
//   - the run's process group is ended on stop, and a run never outlives the app.
//
// Nothing cmdrd sends is argv. Design: docs/plans/2026-11-09-mac-operator.md.

import { createHash, generateKeyPairSync, createPrivateKey, sign as edSign, type KeyObject } from 'node:crypto'
import { join } from 'node:path'

/** An offer from cmdrd: ids and the hash of the brief, nothing to run. */
export interface PlaceOffer {
  type: 'offer'
  run_id: number
  project?: string
  task_id?: string
  step_id?: string
  prod_gate?: string
  brief_sha: string
}

/** The run as cmdrd answers a fetch: text and ids. */
export interface PlaceRunInfo {
  run_id: number
  place: string
  project: string
  task_id: string
  task_title: string
  step_id: string
  step_title: string
  brief: string
  brief_sha: string
  prod_gate?: string
  prod_scope?: string
  role: string
  manager_role: string
}

export interface PlaceMessage {
  type: string
  nonce?: string
  run_id?: number
  run?: PlaceRunInfo
  token?: string
  error?: string
  place?: string
  by?: string
  brief_sha?: string
}

/** What main knows about this Mac and its tools. */
export interface PlaceConfig {
  apiUrl: string // cmdrd's base URL as this Mac reaches it
  cmdr: string // absolute path of the Mac's cmdr
  claude: string // absolute path of the user's claude
  runsDir: string // ~/Library/Application Support/Commander/runs
}

export const DIALOG_TIMEOUT_MS = 10 * 60_000
export const HEARTBEAT_MS = 30_000

/** Tools the place run may call at all; the guard still judges each call. */
export const ALLOWED_TOOLS = 'Bash,Read,Grep,Glob,TodoWrite'
/** Never available, whatever the guard says. */
export const DISALLOWED_TOOLS = 'Edit,Write,MultiEdit,NotebookEdit,WebFetch,WebSearch,Task'

export function sha256(text: string): string {
  return createHash('sha256').update(text, 'utf8').digest('hex')
}

// --- device key ------------------------------------------------------------

/** The sealed store for the device's private key (safeStorage on macOS, which uses the Keychain). */
export interface KeySeal {
  read(): Buffer | null
  write(sealed: Buffer): void
  encrypt(plain: string): Buffer
  decrypt(sealed: Buffer): string
}

export interface DeviceKey {
  publicKey: string // base64 raw Ed25519
  sign(message: string): string // base64 signature
}

/** loadDeviceKey returns the Mac's key pair, creating and sealing one on first use. */
export function loadDeviceKey(seal: KeySeal): DeviceKey {
  let priv: KeyObject
  const sealed = seal.read()
  if (sealed) {
    priv = createPrivateKey(seal.decrypt(sealed))
  } else {
    const pair = generateKeyPairSync('ed25519')
    priv = pair.privateKey
    seal.write(seal.encrypt(priv.export({ type: 'pkcs8', format: 'pem' }).toString()))
  }
  const jwk = priv.export({ format: 'jwk' }) as { x?: string }
  const publicKey = Buffer.from(jwk.x ?? '', 'base64url').toString('base64')
  return { publicKey, sign: (m) => edSign(null, Buffer.from(m, 'utf8'), priv).toString('base64') }
}

/** challengeMessage matches place.ChallengeMessage on cmdrd. */
export function challengeMessage(nonce: string, publicKey: string): string {
  return `commander-place-hello-v1\n${publicKey}\n${nonce}`
}

export function helloFor(key: DeviceKey, nonce: string, cmdrVersion: string, appVersion: string): Record<string, string> {
  return { type: 'hello', public_key: key.publicKey, signature: key.sign(challengeMessage(nonce, key.publicKey)), cmdr_version: cmdrVersion, app_version: appVersion }
}

// --- the fixed launch template ----------------------------------------------

/** placeSettings are the run's Claude hooks: the guard before EVERY tool, the mail hook after each and at Stop. */
export function placeSettings(cmdr: string, role: string): string {
  const q = shQuote
  const guard = `${q(cmdr)} internal-operator-guard --role ${q(role)} --place read-only`
  const mail = (ev: string) => `${q(cmdr)} internal-mail-hook --role ${q(role)} --event ${ev} --place 2>/dev/null || true`
  return JSON.stringify({
    hooks: {
      PreToolUse: [{ hooks: [{ type: 'command', command: guard }] }],
      PostToolUse: [{ hooks: [{ type: 'command', command: mail('PostToolUse') }] }],
      Stop: [{ hooks: [{ type: 'command', command: mail('Stop') }] }],
    },
  })
}

/** bootstrapPrompt is the run's first message, built from the fetched bead fields only. */
export function bootstrapPrompt(run: PlaceRunInfo): string {
  const lines = [
    `You are the Operator for task ${run.task_id} (step ${run.step_id}: ${oneLine(run.step_title)}) in project "${run.project}", running read-only on the user's Mac (${run.place}).`,
    `Your Manager's mail role is ${run.manager_role}. Report only with cmdr place comment|mail|report; write your report draft into your run directory (your working directory).`,
    '',
    `Task: ${oneLine(run.task_title)}`,
    '',
    'Brief:',
    run.brief,
  ]
  if (run.prod_gate) {
    lines.push('', `PRODUCTION GATE ${run.prod_gate}: the user approved exactly this read-only production action, and nothing more:`, run.prod_scope ?? '')
  } else {
    lines.push('', 'Production is forbidden in this run: no production CLI or data.')
  }
  return lines.join('\n')
}

export interface Launch {
  command: string
  args: string[]
  env: Record<string, string>
  cwd: string
}

/**
 * buildLaunch is the only command line a place run gets. The offer and the
 * fetched run contribute text (the bootstrap prompt) and ids (role, run id,
 * prod-gate id) and nothing else: no flag, tool, path or command.
 */
export function buildLaunch(cfg: PlaceConfig, run: PlaceRunInfo, token: string, files: { runDir: string; settingsPath: string; rolePrompt: string; sessionId: string }, base: NodeJS.ProcessEnv): Launch {
  const env: Record<string, string> = {}
  for (const k of ['PATH', 'HOME', 'USER', 'LOGNAME', 'SHELL', 'LANG', 'TMPDIR', 'TERM']) {
    const v = base[k]
    if (v) env[k] = v
  }
  // The user's own login (~/.claude): never an API key, never another account dir.
  Object.assign(env, {
    COMMANDER_ROLE: run.role,
    COMMANDER_MANAGER_ROLE: run.manager_role,
    COMMANDER_PLACE_RUN: String(run.run_id),
    COMMANDER_PLACE_TOKEN: token,
    COMMANDER_PLACE_URL: cfg.apiUrl,
    COMMANDER_PLACE_DIR: files.runDir,
  })
  if (run.prod_gate) env.COMMANDER_PROD_GATE = run.prod_gate
  return {
    command: cfg.claude,
    args: [
      '-p',
      bootstrapPrompt(run),
      '--output-format',
      'stream-json',
      '--verbose',
      '--session-id',
      files.sessionId,
      '--allowedTools',
      ALLOWED_TOOLS,
      '--disallowedTools',
      DISALLOWED_TOOLS,
      '--strict-mcp-config',
      '--settings',
      files.settingsPath,
      '--append-system-prompt',
      files.rolePrompt,
    ],
    env,
    cwd: files.runDir,
  }
}

// --- the dialog ---------------------------------------------------------------

/** dialogText is what the native dialog shows: where, which step, the whole brief, the production scope. */
export function dialogText(run: PlaceRunInfo): { message: string; detail: string } {
  const prod = run.prod_gate
    ? `\n\nPRODUCTION (gate ${run.prod_gate}): the user approved only this:\n${run.prod_scope ?? ''}`
    : '\n\nNo production access: production CLIs are refused.'
  return {
    message: `Run a read-only Operator on this Mac for ${oneLine(run.step_title)}?`,
    detail:
      `Project ${run.project}, task ${run.task_id}: ${oneLine(run.task_title)}\nManager: ${run.manager_role}\n\nBrief:\n${clip(run.brief, 4000)}${prod}\n\n` +
      'It runs your own claude, read-only: no edit tools, every command checked against ~/.commander/place-policy.toml, ' +
      'writes only in its own run folder. Quitting the app stops it.',
  }
}

// --- usage ---------------------------------------------------------------------

/** usageOf reads one stream-json line: a rate limit event to forward, and whether the run hit its usage limit. */
export function usageOf(line: string): { event?: unknown; limit: boolean } {
  let m: { type?: string; subtype?: string; rate_limit_info?: { status?: string }; error?: string; message?: { content?: { text?: string }[] } }
  try {
    m = JSON.parse(line)
  } catch {
    return { limit: false }
  }
  if (m.type === 'rate_limit_event') {
    const status = m.rate_limit_info?.status
    return { event: m, limit: status === 'rejected' }
  }
  if (m.type === 'assistant' && (m.error === 'rate_limit' || m.error === 'billing_error')) return { event: m, limit: true }
  return { limit: false }
}

// --- the runner ------------------------------------------------------------------

export interface ChildLike {
  pid?: number
  stdout?: { on(ev: 'data', cb: (b: Buffer) => void): void } | null
  on(ev: 'error', cb: (error: Error) => void): void
  on(ev: 'exit', cb: (code: number | null, signal: string | null) => void): void
}

export interface PlaceRunnerDeps {
  cfg: PlaceConfig
  /** Ask cmdrd over the channel and wait for its answer (fetch, accept). */
  ask(msg: Record<string, unknown>): Promise<PlaceMessage>
  /** Send without waiting (decline). */
  send(msg: Record<string, unknown>): void
  /** The native dialog: true only for "Run on this Mac". */
  confirm(message: string, detail: string, timeoutMs: number): Promise<boolean>
  /** Call a run endpoint with the run token; resolves to the HTTP status (rejects only when cmdrd was not reached). */
  post(runId: number, token: string, path: 'heartbeat' | 'exit', body: unknown): Promise<number>
  /** Make the run directory (0700) and its launch files outside it. */
  prepare(runId: number, settings: string): { runDir: string; settingsPath: string }
  rolePrompt(): Promise<string>
  spawn(l: Launch): ChildLike
  /** End a process group. */
  killGroup(pid: number): void | Promise<void>
  /** Guard refusals logged since the last call. */
  refusals(): string[]
  newSessionId(): string
  env?: NodeJS.ProcessEnv
  setInterval?: (fn: () => void, ms: number) => unknown
  clearInterval?: (h: unknown) => void
  log?: (msg: string) => void
}

interface Active {
  runId: number
  token: string
  stepTitle: string
  child: ChildLike
  timer: unknown
  usage: unknown[]
  limit: boolean
  stopped?: string
  cleanup?: Promise<void>
  lastTool: number
  tokens: number
}

export function createPlaceRunner(d: PlaceRunnerDeps) {
  const every = d.setInterval ?? ((fn: () => void, ms: number) => setInterval(fn, ms))
  const stopEvery = d.clearInterval ?? ((h: unknown) => clearInterval(h as ReturnType<typeof setInterval>))
  const log = d.log ?? (() => {})
  const seen = new Set<number>() // a run is asked about once: a new run needs a new offer
  const active = new Map<number, Active>()
  const pending = new Map<number, { step: string; stopped?: string; cancel?: () => void }>()
  const cleanups = new Set<Promise<void>>()
  const reports = new Set<Promise<void>>()
  let closing = false

  const beat = async (a: Active) => {
    const refusals = d.refusals()
    const usage = a.usage.splice(0)
    try {
      const status = await d.post(a.runId, a.token, 'heartbeat', { pid: a.child.pid ?? 0, alive: true, last_tool: a.lastTool, tokens: a.tokens, refusals, usage, usage_limit: a.limit })
      if (status === 401) {
        // The token is dead: cmdrd ended the run (revoked, given up as died,
        // expired, stopped with the stop frame lost). Nothing may run on.
        log(`place: run ${a.runId} ended on cmdrd's side (401): stopping it`)
        stop(a.runId, 'cmdrd (the run ended on the server)')
      } else if (status !== 200) {
        log(`place: heartbeat of run ${a.runId} answered ${status}`)
      }
    } catch (e) {
      log(`place: heartbeat of run ${a.runId} failed: ${(e as Error).message}`)
    }
    if (a.limit && !a.stopped) stop(a.runId, 'usage limit')
  }

  const stop = (runId: number, by: string) => {
    const p = pending.get(runId)
    if (p) { p.stopped = by; p.cancel?.() }
    const a = active.get(runId)
    if (!a || a.stopped) return
    a.stopped = by
    if (a.child.pid) {
      const cleanup = Promise.resolve(d.killGroup(a.child.pid))
      a.cleanup = cleanup
      cleanups.add(cleanup)
      void cleanup.then(() => cleanups.delete(cleanup), (e) => log(`place: process cleanup failed: ${String(e)}`))
    }
  }

  const onOffer = async (offer: PlaceOffer) => {
    if (seen.has(offer.run_id) || active.size > 0 || pending.size > 0 || closing) {
      if (!seen.has(offer.run_id)) d.send({ type: 'decline', run_id: offer.run_id, reason: 'another run is running on this Mac' })
      return
    }
    seen.add(offer.run_id)
    const p: { step: string; stopped?: string; cancel?: () => void } = { step: 'pending run' }
    pending.set(offer.run_id, p)
    try {
      const got = await d.ask({ type: 'fetch', run_id: offer.run_id })
      const run = got.run
      if (got.type !== 'run' || !run) {
        log(`place: run ${offer.run_id} could not be fetched: ${got.error ?? got.type}`)
        return
      }
      // What the dialog shows must be what was offered and what runs.
      if (sha256(run.brief) !== offer.brief_sha || run.brief_sha !== offer.brief_sha || run.run_id !== offer.run_id) {
        d.send({ type: 'decline', run_id: offer.run_id, reason: 'the brief does not match the offer' })
        return
      }
      p.step = run.step_title
      if (p.stopped || closing) return
      const text = dialogText(run)
      if (!(await d.confirm(text.message, text.detail, DIALOG_TIMEOUT_MS))) {
        d.send({ type: 'decline', run_id: offer.run_id, reason: 'the user did not start it on the Mac' })
        return
      }
      if (p.stopped || closing) return
      // scope_sha: cmdrd refuses the accept when the prod gate's scope is no
      // longer the one this dialog showed (and the launch quotes).
      const acc = await d.ask({ type: 'accept', run_id: offer.run_id, brief_sha: offer.brief_sha, scope_sha: sha256(run.prod_scope ?? '') })
      if (acc.type !== 'accepted' || !acc.token) {
        log(`place: run ${offer.run_id} not accepted: ${acc.error ?? acc.type}`)
        return
      }
      // An accepted token must reach a terminal outcome even if no child starts.
      let a: Active | undefined
      let finished = false
      const finish = (status: string, reason: string) => {
        if (finished) return
        finished = true
        if (a) {
          if (a.timer !== undefined) stopEvery(a.timer)
          const ended = a
          if (ended.cleanup) void ended.cleanup.then(() => active.delete(ended.runId), () => {})
          else active.delete(ended.runId)
        }
        const report = d.post(run.run_id, acc.token!, 'exit', { status, reason })
          .then((code) => { if (code !== 200) log(`place: exit of run ${run.run_id} answered ${code}`) })
          .catch((e) => log(`place: exit of run ${run.run_id} not sent: ${(e as Error).message}`))
        reports.add(report)
        void report.then(() => reports.delete(report))
      }
      p.cancel = () => finish('stopped', `stopped by ${p.stopped ?? 'the app quitting'}`)
      if (p.stopped || closing) { p.cancel(); return }
      let child: ChildLike
      try {
        const files = d.prepare(run.run_id, placeSettings(d.cfg.cmdr, run.role))
        const rolePrompt = await d.rolePrompt()
        if (p.stopped || closing) {
          finish('stopped', `stopped by ${p.stopped ?? 'the app quitting'}`)
          return
        }
        const launch = buildLaunch(d.cfg, run, acc.token, { ...files, rolePrompt, sessionId: d.newSessionId() }, d.env ?? process.env)
        child = d.spawn(launch)
      } catch (e) {
        finish('stopped', `could not start claude: ${(e as Error).message}`)
        return
      }
      const running: Active = { runId: run.run_id, token: acc.token, stepTitle: run.step_title, child, timer: undefined, usage: [], limit: false, lastTool: 0, tokens: 0 }
      a = running
      active.set(run.run_id, running)
      pending.delete(run.run_id)
      child.on('error', (error) => {
        stop(run.run_id, 'child error')
        finish('stopped', `claude failed: ${error.message}`)
      })
      child.on('exit', (code, signal) => {
        finish(running.stopped ? 'stopped' : 'exited', running.stopped ? `stopped by ${running.stopped}` : `claude exited (${signal ?? code})`)
      })
      let rest = ''
      child.stdout?.on('data', (b) => {
        const parts = (rest + b.toString('utf8')).split('\n')
        rest = parts.pop() ?? ''
        for (const line of parts) {
          const u = usageOf(line)
          if (u.event) running.usage.push(u.event)
          if (u.limit) running.limit = true
          if (line.includes('"tool_use"')) running.lastTool = Math.floor(Date.now() / 1000)
        }
      })
      if (!finished) {
        running.timer = every(() => { if (!finished) void beat(running) }, HEARTBEAT_MS)
        void beat(running)
      }
    } finally {
      pending.delete(offer.run_id)
    }
  }

  return {
    /** onMessage handles one frame from cmdrd. */
    onMessage(m: PlaceMessage) {
      if (m.type === 'offer') void onOffer(m as unknown as PlaceOffer).catch((e) => log(`place: offer ${m.run_id}: ${(e as Error).message}`))
      else if (m.type === 'stop' && m.run_id) stop(m.run_id, m.by || 'cmdrd')
    },
    stop,
    /** active lists the running runs' steps, for the quit prompt. */
    active(): { runId: number; step: string }[] {
      return [...active.values()].map((a) => ({ runId: a.runId, step: a.stepTitle })).concat([...pending].map(([runId, p]) => ({ runId, step: p.step })))
    },
    /** stopAll ends every run (the app is quitting). */
    async stopAll(by = 'the app quitting') {
      closing = true
      for (const id of pending.keys()) stop(id, by)
      for (const id of active.keys()) stop(id, by)
      await Promise.all([...cleanups, ...reports])
    },
  }
}

/** quitPrompt is the question asked before quitting with runs on this Mac, or null when none run. */
export function quitPrompt(runs: { step: string }[]): { message: string; detail: string } | null {
  if (runs.length === 0) return null
  return {
    message: runs.length === 1 ? `Stop the run on ${oneLine(runs[0].step)}?` : `Stop the ${runs.length} runs on this Mac?`,
    detail: 'A run on this Mac does not outlive the app: quitting stops it, and its step goes back to its Manager. Cancel keeps the app open.',
  }
}

/** runPaths are where a run lives: its private directory, and its launch files beside it (outside what the agent may write). */
export function runPaths(runsDir: string, runId: number): { runDir: string; launchDir: string; settingsPath: string; urlPath: string } {
  const launchDir = join(runsDir, `${runId}.launch`)
  // urlPath pins the server the run verbs may talk to (cmdr reads it beside
  // the run directory and refuses a COMMANDER_PLACE_URL that differs).
  return { runDir: join(runsDir, String(runId)), launchDir, settingsPath: join(launchDir, 'settings.json'), urlPath: join(launchDir, 'place-url') }
}

export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

function oneLine(s: string): string {
  return clip(s.replace(/[\p{Cc}\p{Cf}\p{Zl}\p{Zp}]/gu, ' ').replace(/\s+/g, ' ').trim(), 160)
}

function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '...' : s
}

// --- the channel ------------------------------------------------------------------

/** The subset of WebSocket the channel uses (the global one in main, a fake in tests). */
export interface SocketLike {
  onopen: (() => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: (() => void) | null
  onerror: (() => void) | null
  send(data: string): void
  close(): void
}

export interface ChannelDeps {
  url: string // ws(s)://.../api/places/connect?token=<api token>
  open(url: string): SocketLike
  key: DeviceKey
  cmdrVersion: string
  appVersion: string
  onWelcome?(place: string): void
  onRefused?(why: string): void
  setTimeout?: (fn: () => void, ms: number) => unknown
  log?: (msg: string) => void
}

const ASK_TIMEOUT_MS = 30_000
const RECONNECT_MS = [2_000, 5_000, 15_000, 60_000]

/**
 * connectPlaceChannel holds the channel to cmdrd: answers the challenge with
 * the device key, hands offers and stops to the runner, and pairs each fetch
 * or accept with its answer. It reconnects with a backoff, and stops trying
 * when cmdrd refuses the Mac (unpaired, revoked, another cmdr revision).
 */
export function connectPlaceChannel(d: ChannelDeps, onMessage: (m: PlaceMessage) => void) {
  const later = d.setTimeout ?? ((fn: () => void, ms: number) => setTimeout(fn, ms))
  const log = d.log ?? (() => {})
  let sock: SocketLike | null = null
  let ready = false
  let closed = false
  let tries = 0
  const waiting: { runId?: number; types: string[]; resolve: (m: PlaceMessage) => void }[] = []

  const connect = () => {
    if (closed) return
    const s = d.open(d.url)
    sock = s
    ready = false
    s.onmessage = (ev) => {
      let m: PlaceMessage
      try {
        m = JSON.parse(String(ev.data))
      } catch {
        return
      }
      if (m.type === 'challenge' && m.nonce) {
        s.send(JSON.stringify(helloFor(d.key, m.nonce, d.cmdrVersion, d.appVersion)))
        return
      }
      if (m.type === 'welcome') {
        ready = true
        tries = 0
        d.onWelcome?.(m.place ?? '')
        return
      }
      if (!ready && m.type === 'error') {
        closed = true // refused: retrying cannot help until the user acts
        d.onRefused?.(m.error ?? 'refused')
        return
      }
      const i = waiting.findIndex((w) => w.types.includes(m.type) && (w.runId === undefined || w.runId === m.run_id))
      if (i >= 0) {
        waiting.splice(i, 1)[0].resolve(m)
        return
      }
      onMessage(m)
    }
    s.onclose = () => {
      ready = false
      for (const w of waiting.splice(0)) w.resolve({ type: 'error', error: 'channel closed' })
      if (closed) return
      const wait = RECONNECT_MS[Math.min(tries++, RECONNECT_MS.length - 1)]
      log(`place: channel closed, reconnecting in ${wait / 1000}s`)
      later(connect, wait)
    }
    s.onerror = () => {}
  }
  connect()

  return {
    ask(msg: Record<string, unknown>): Promise<PlaceMessage> {
      if (!sock || !ready) return Promise.resolve({ type: 'error', error: 'not connected' })
      const runId = typeof msg.run_id === 'number' ? msg.run_id : undefined
      const types = msg.type === 'fetch' ? ['run', 'error'] : msg.type === 'accept' ? ['accepted', 'error'] : ['error', 'pong']
      return new Promise((resolve) => {
        const entry = { runId, types, resolve }
        waiting.push(entry)
        later(() => {
          const i = waiting.indexOf(entry)
          if (i >= 0) waiting.splice(i, 1)[0].resolve({ type: 'error', error: 'cmdrd did not answer' })
        }, ASK_TIMEOUT_MS)
        sock!.send(JSON.stringify(msg))
      })
    },
    send(msg: Record<string, unknown>) {
      if (sock && ready) sock.send(JSON.stringify(msg))
    },
    close() {
      closed = true
      sock?.close()
    },
  }
}
