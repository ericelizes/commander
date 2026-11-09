#!/usr/bin/env node
// Headless smoke: serve the built renderer (dist/) as a plain web page, load it
// in chrome-headless-shell (no window, no Dock icon), wait until it shows the
// expected connection state, screenshot it, exit 0 (pass) or 1.
//
//   node scripts/smoke.mjs                      live: the real cmdrd ($COMMANDER_HOME/api-url + api-token)
//   node scripts/smoke.mjs --offline            a fake cmdrd sends the fixture once, then goes away
//   node scripts/smoke.mjs --fake               a fake cmdrd sends the fixture and stays: any snapshot, live
//   node scripts/smoke.mjs --served             the page cmdrd itself serves at <api-url>/app/ (needs app/dist built)
//   --out <png>  --fixture <json>  --dist <dir> (default dist)  --width 1440 --height 900 --scale 2
//   --theme system|light|dark|glass  --scheme dark|light (the OS preference)  --query 'ws=2&ask=1'
//   --eval '<js>' (repeatable) runs in the page before the shot
//   --which-browser  print the browser it would use and exit (1 when none)
//   --expect '<js>' (repeatable) runs after the evals; the smoke fails unless it returns a truthy value (printed)
//   --answer-ms 30000  how long a browser call may go unanswered
//
// A thrown error, an unhandled rejection, console.error or an error the browser logs (a failed load),
// up to LATE_ERROR_MS after the shot, fails the smoke in every mode, unless expectedPageError below names it.
// So does a browser call left unanswered (CDP_ANSWER_MS), naming the call.
//
// Browser: $CHROME_HEADLESS_SHELL, else ~/.commander/browsers/chrome-headless-shell/*/*/chrome-headless-shell,
// else (Linux only) a system Chrome/Chromium, run with --headless.

import { spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { homedir, tmpdir } from 'node:os'
import { dirname, extname, join, resolve } from 'node:path'

const argv = process.argv.slice(2)
const opt = (name, def) => {
  const i = argv.indexOf(name)
  return i >= 0 ? argv[i + 1] : def
}
const offline = argv.includes('--offline')
const fake = argv.includes('--fake')
const servedByCmdrd = argv.includes('--served')
const out = resolve(opt('--out', offline ? 'out/smoke-offline.png' : 'out/smoke.png'))
const fixture = resolve(opt('--fixture', 'test/fixtures/snapshot.json'))
const width = Number(opt('--width', 1440))
const height = Number(opt('--height', 900))
const scale = Number(opt('--scale', 2))
const dist = resolve(opt('--dist', 'dist'))
const params = new URLSearchParams(opt('--query', ''))
if (opt('--theme')) params.set('theme', opt('--theme'))
const search = params.size ? `?${params}` : ''

function fail(msg) {
  console.error(`smoke: FAIL: ${msg}`)
  process.exitCode = 1
}

function findShell() {
  if (process.env.CHROME_HEADLESS_SHELL) return process.env.CHROME_HEADLESS_SHELL
  const root = join(homedir(), '.commander', 'browsers', 'chrome-headless-shell')
  if (existsSync(root)) {
    for (const ver of readdirSync(root).sort().reverse()) {
      for (const plat of readdirSync(join(root, ver))) {
        const bin = join(root, ver, plat, 'chrome-headless-shell')
        if (existsSync(bin)) return bin
      }
    }
  }
  // Never Google Chrome.app on a Mac (it would show on the user's screen); a
  // Linux box often has only a system Chrome, which runs headless here.
  if (process.platform === 'linux') {
    for (const bin of ['/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser']) if (existsSync(bin)) return bin
  }
  return null
}

const commanderHome = process.env.COMMANDER_HOME || join(homedir(), '.commander')
const read = (p) => {
  try {
    return readFileSync(p, 'utf8').trim()
  } catch {
    return ''
  }
}

const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json' }

// A fake cmdrd for --offline: the first /api/events gets the fixture, then the
// socket drops and every later attempt is refused, so the page goes offline.
let served = false
function fakeEvents(req, socket) {
  if (served && !fake) {
    socket.end('HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\n\r\n')
    return
  }
  served = true
  const accept = createHash('sha1').update(req.headers['sec-websocket-key'] + '258EAFA5-E914-47DA-95CA-C5AB0DC85B11').digest('base64')
  socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`)
  const payload = Buffer.from(JSON.stringify({ type: 'snapshot', snapshot: JSON.parse(readFileSync(fixture, 'utf8')) }))
  const len = payload.length
  let head
  if (len < 126) head = Buffer.from([0x81, len])
  else if (len < 65536) head = Buffer.from([0x81, 126, len >> 8, len & 255])
  else {
    head = Buffer.alloc(10)
    head[0] = 0x81
    head[1] = 127
    head.writeBigUInt64BE(BigInt(len), 2)
  }
  socket.write(Buffer.concat([head, payload]))
  if (!fake) setTimeout(() => socket.destroy(), 400)
}

// Overview's recap is a read that travels as a POST (the body says which workstream); it changes nothing.
const isWrite = (method, path) => method !== 'GET' && method !== 'HEAD' && !(method === 'POST' && path === '/api/overview')

const server = createServer((req, res) => {
  const path = decodeURIComponent(new URL(req.url, 'http://x').pathname)
  // The allow-list below is for reads, and the browser's 404 line names no method: a write is seen here.
  if (isWrite(req.method, path)) notePageError(`${req.method} ${path}: the fake cmdrd takes no writes`)
  if (fake && path === '/api/linear') {
    const snapshot = JSON.parse(readFileSync(fixture, 'utf8'))
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify(snapshot.linear ?? { state: 'no_key', key: 'not set', issues: [] }))
    return
  }
  if (fake && path === '/api/overview') {
    // The fixture's `overview` is one stream line: a recap still being written.
    const snapshot = JSON.parse(readFileSync(fixture, 'utf8'))
    if (snapshot.overview) {
      res.writeHead(200, { 'Content-Type': 'application/x-ndjson' }).end(JSON.stringify(snapshot.overview) + '\n')
      return
    }
  }
  const file = join(dist, path === '/' ? 'index.html' : path)
  if (!file.startsWith(dist) || !existsSync(file)) {
    res.writeHead(404).end()
    return
  }
  res.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream', 'Cache-Control': 'no-store' })
  res.end(readFileSync(file))
})
server.on('upgrade', (req, socket) => ((offline || fake) && req.url.startsWith('/api/events') ? fakeEvents(req, socket) : socket.destroy()))

// Errors a right run still logs, each exact: one route or one message, never a class, so a
// missing script or a 404 from any other route still fails. Add an entry only with its reason.
const LOAD_404 = 'Failed to load resource: the server responded with a status of 404 (Not Found)'
function expectedPageError(text, url, origin, pageOrigin) {
  const path = url.startsWith(`${origin}/`) ? new URL(url).pathname : ''
  // The page never asks for an icon; the browser does, at wherever the page came from
  // (--served: cmdrd), and neither the smoke's server nor cmdrd has one.
  if (text === LOAD_404 && url === `${pageOrigin}/favicon.ico`) return true
  if (!offline && !fake) return false // a real cmdrd serves everything below: there it is a real error
  // The fake cmdrd serves the snapshot and nothing else, and the page reads these at load
  // and when a workstream opens; it copes with their 404 and a real cmdrd answers them.
  if (text === LOAD_404 && ['/api/update', '/api/settings', '/api/chat', '/api/tabs', '/api/overview'].includes(path)) return true
  // --fake serves the fixture's /api/linear; --offline has none.
  if (text === LOAD_404 && path === '/api/linear' && offline) return true
  const ws = origin.replace('http', 'ws')
  // Sockets by route: the browser words the same failure differently from run to run.
  // The upgrade handler above takes only /api/events, so the app's capture socket is dropped.
  if (text.startsWith(`WebSocket connection to '${ws}/api/app/connect?token=smoke&kind=web' failed:`)) return true
  // What --offline is for: every attempt after the first is refused.
  return offline && text.startsWith(`WebSocket connection to '${ws}/api/events?token=smoke' failed:`)
}

const LATE_ERROR_MS = 2000
const pageErrors = []
function notePageError(line) {
  if (!pageErrors.includes(line)) pageErrors.push(line)
}

// Every call to the browser has this long to answer. It can leave one unanswered, a poll on a
// page that never connected or a capture on a page left idle, and such a run has to end with the
// errors it has, not hang. Long enough for a loaded machine: a call is answered in milliseconds.
const CDP_ANSWER_MS = Number(opt('--answer-ms', 30_000))

async function cdp(wsUrl) {
  const ws = new WebSocket(wsUrl)
  let openTimer
  await new Promise((ok, ko) => {
    openTimer = setTimeout(() => ko(new Error(`the browser did not open its debug socket in ${CDP_ANSWER_MS / 1000}s`)), CDP_ANSWER_MS)
    ws.onopen = ok
    ws.onerror = () => ko(new Error('the browser refused its debug socket'))
  }).finally(() => clearTimeout(openTimer))
  let id = 0
  const pending = new Map()
  const listeners = new Map()
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data)
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg)
      pending.delete(msg.id)
    } else if (msg.method) listeners.get(msg.method)?.(msg.params)
  }
  const send = (method, params = {}) =>
    new Promise((ok, ko) => {
      const n = ++id
      const unanswered = setTimeout(() => {
        pending.delete(n)
        ko(new Error(`the browser did not answer ${method} in ${CDP_ANSWER_MS / 1000}s`))
      }, CDP_ANSWER_MS)
      pending.set(n, (m) => {
        clearTimeout(unanswered)
        m.error ? ko(new Error(`${method}: ${m.error.message}`)) : ok(m.result)
      })
      ws.send(JSON.stringify({ id: n, method, params }))
    })
  return { send, on: (method, f) => listeners.set(method, f), close: () => ws.close() }
}

async function main() {
  if (!existsSync(join(dist, 'index.html'))) return fail('dist/index.html missing: run `npm run build` first')
  const shell = findShell()
  if (!shell) return fail('chrome-headless-shell not found (set CHROME_HEADLESS_SHELL)')

  await new Promise((ok) => server.listen(0, '127.0.0.1', ok))
  const origin = `http://127.0.0.1:${server.address().port}`
  let hash
  let pageURL = ''
  if (offline || fake) hash = `#token=smoke`
  else if (servedByCmdrd) {
    const token = read(join(commanderHome, 'api-token'))
    const api = read(join(commanderHome, 'api-url')) || 'http://127.0.0.1:8787'
    pageURL = `${api}/app/#token=${encodeURIComponent(token)}`
  }
  else {
    const token = read(join(commanderHome, 'api-token'))
    const api = read(join(commanderHome, 'api-url')) || 'http://127.0.0.1:8787'
    if (!token) return fail(`no token in ${commanderHome}/api-token`)
    hash = `#token=${encodeURIComponent(token)}&api=${encodeURIComponent(api)}`
  }

  const profile = mkdtempSync(join(tmpdir(), 'cmdr-smoke-'))
  const chrome = spawn(shell, ['--headless', '--disable-gpu', '--hide-scrollbars', '--no-first-run', '--remote-debugging-port=0', `--user-data-dir=${profile}`, `--window-size=${width},${height}`, 'about:blank'], {
    stdio: ['ignore', 'ignore', 'pipe'],
    // Its own process group, so the kill below also ends what a wrapper script (Linux google-chrome) started.
    detached: true,
  })
  // A caller ending this run (a test deadline) must not leave the browser behind.
  // Kept for every signal: a second one (the group's and a forwarded copy) must
  // not fall back to the default exit before the browser is gone.
  const stop = () => {
    try {
      process.kill(-chrome.pid, 'SIGKILL')
    } catch {}
    process.exit(1)
  }
  for (const sig of ['SIGTERM', 'SIGINT', 'SIGHUP']) process.on(sig, stop)
  let want = ''
  let state = ''
  try {
    const browserWS = await new Promise((ok, ko) => {
      let buf = ''
      const t = setTimeout(() => ko(new Error('headless shell did not start')), 15_000)
      chrome.stderr.on('data', (d) => {
        buf += d
        const m = /DevTools listening on (ws:\/\/\S+)/.exec(buf)
        if (m) {
          clearTimeout(t)
          ok(m[1])
        }
      })
    })
    const port = new URL(browserWS).port
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`, { signal: AbortSignal.timeout(CDP_ANSWER_MS) })).json()
    const page = targets.find((t) => t.type === 'page')
    const c = await cdp(page.webSocketDebuggerUrl)
    await c.send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: scale, mobile: false })
    await c.send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: opt('--scheme', 'dark') }] })
    // Before the page loads: an error in its first script counts.
    c.on('Runtime.exceptionThrown', ({ exceptionDetails: d }) => notePageError(`uncaught: ${d.exception?.description ?? d.exception?.value ?? d.text}`))
    c.on('Runtime.consoleAPICalled', ({ type, args }) => {
      if (type === 'error' || type === 'assert') notePageError(`console.${type}: ${args.map((a) => a.description ?? a.value ?? a.type).join(' ')}`)
    })
    c.on('Log.entryAdded', ({ entry }) => {
      if (entry.level === 'error' && !expectedPageError(entry.text, entry.url ?? '', origin, pageURL ? new URL(pageURL).origin : origin)) notePageError(`browser: ${entry.text} (${entry.url})`)
    })
    await c.send('Runtime.enable')
    await c.send('Log.enable')
    await c.send('Page.enable')
    await c.send('Page.navigate', { url: pageURL ? pageURL.replace('#', `${search}#`) : `${origin}/${search}${hash}` })

    // The built-in chat fixture owns its connection and stays live offline.
    const expectOffline = offline && params.get('fixture') !== 'chat'
    want = expectOffline ? 'offline' : 'live'
    const deadline = Date.now() + 20_000
    while (Date.now() < deadline) {
      const r = await c.send('Runtime.evaluate', { expression: 'document.documentElement.dataset.conn || ""', returnByValue: true })
      state = r.result.value
      // Offline shows the start screen ("Restarting Commander") after its short grace.
      const banner = expectOffline ? (await c.send('Runtime.evaluate', { expression: '!!document.querySelector(".start-screen.on")', returnByValue: true })).result.value : true
      if (state === want && banner) break
      await new Promise((r) => setTimeout(r, 150))
    }
    // --press 8 (repeatable): Alt+n, the plain-page form of Ctrl/Cmd+n.
    for (let i = 0; i < argv.length; i++) {
      if (argv[i] !== '--press') continue
      await c.send('Runtime.evaluate', { expression: `window.dispatchEvent(new KeyboardEvent('keydown', {key: ${JSON.stringify(argv[i + 1])}, altKey: true}))` })
      await new Promise((r) => setTimeout(r, 100))
    }
    // --eval <js> (repeatable): run in the page before the shot, e.g. type and send in a fixture=chat composer.
    for (let i = 0; i < argv.length; i++) {
      if (argv[i] !== '--eval') continue
      await c.send('Runtime.evaluate', { expression: argv[i + 1], awaitPromise: true })
      await new Promise((r) => setTimeout(r, 300))
    }
    for (let i = 0; i < argv.length; i++) {
      if (argv[i] !== '--expect') continue
      const r = await c.send('Runtime.evaluate', { expression: argv[i + 1], awaitPromise: true, returnByValue: true })
      console.log(`smoke: expect -> ${JSON.stringify(r.result.value)}`)
      if (!r.result.value) fail(`expectation false: ${argv[i + 1].slice(0, 120)}`)
    }
    await new Promise((r) => setTimeout(r, 400)) // let the dropdown animation settle
    const facts = (
      await c.send('Runtime.evaluate', {
        expression: `JSON.stringify({ws: document.querySelectorAll('.ws').length, asks: document.querySelectorAll('.row.ask').length, steps: document.querySelectorAll('.step').length, offline: !!document.querySelector('.start-screen.on')})`,
        returnByValue: true,
      })
    ).result.value
    const shot = await c.send('Page.captureScreenshot', { format: 'png' })
    mkdirSync(dirname(out), { recursive: true })
    writeFileSync(out, Buffer.from(shot.data, 'base64'))
    // A page's first seconds often end after the first paint: keep listening. Later than this, an error goes unseen.
    await new Promise((r) => setTimeout(r, LATE_ERROR_MS))
    c.close()
    const f = JSON.parse(facts)
    console.log(`smoke: conn=${state} ${facts} -> ${out}`)
    if (state !== want) fail(`expected conn=${want}, got ${state || 'nothing'}`)
    else if (f.ws === 0) fail('no workstreams rendered')
    else if (expectOffline && !f.offline) fail('start screen missing while offline')
  } catch (e) {
    // A call left unanswered on a page that never connected: the page is the likelier cause, so it reads first.
    fail(want && state !== want ? `expected conn=${want}, got ${state || 'nothing'} (${e.message})` : e.message)
  } finally {
    // On every way out, an unanswered call included: the page's own errors are the likely cause.
    for (const e of pageErrors) fail(`page error: ${e}`)
    if (!process.exitCode) console.log('smoke: pass')
    const exited = chrome.exitCode !== null ? Promise.resolve() : new Promise((r) => chrome.once('exit', r))
    try {
      process.kill(-chrome.pid, 'SIGKILL')
    } catch {
      chrome.kill('SIGKILL')
    }
    server.close()
    await Promise.race([exited, new Promise((r) => setTimeout(r, 3000))])
    try {
      rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 })
    } catch (e) {
      console.warn(`smoke: warning: could not remove the browser profile ${profile}: ${e.message}`)
    }
  }
}

// --which-browser: print the browser the smoke would use and exit (1 when none), so a test can skip with a reason.
if (argv.includes('--which-browser')) {
  const shell = findShell()
  console.log(shell ?? '')
  process.exitCode = shell ? 0 : 1
} else {
  // Explicitly: a helper the browser left behind can hold its stderr pipe, and the run open, forever.
  main()
    .catch((e) => fail(e.message))
    .finally(() => process.exit())
}
