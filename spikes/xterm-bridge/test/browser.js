// Real browser check: headless Chrome loads the page, driven over CDP.
// Measures keystroke -> rendered-in-xterm.js latency, lone-Esc delay, which
// terminal queries real xterm.js answers, and saves a screenshot.
'use strict'
const path = require('path')
const fs = require('fs')
const os = require('os')
const { spawn, execFileSync } = require('child_process')
const WebSocket = require('ws')
const { sleep, PORT } = require('./lib')

const CHROME = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'
const OUT = process.env.OUT || path.join(__dirname, '../evidence')

async function cdp() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'xb-chrome-'))
  const chrome = spawn(CHROME, ['--headless=new', '--remote-debugging-port=9333', `--user-data-dir=${dir}`, '--window-size=1000,620', 'about:blank'], { stdio: 'ignore' })
  let targets
  for (let i = 0; i < 50; i++) {
    try { targets = await (await fetch('http://127.0.0.1:9333/json')).json(); break } catch { await sleep(100) }
  }
  const page = targets.find((t) => t.type === 'page')
  const ws = new WebSocket(page.webSocketDebuggerUrl)
  await new Promise((r) => ws.on('open', r))
  let id = 0
  const pending = new Map()
  ws.on('message', (m) => { const d = JSON.parse(m); if (pending.has(d.id)) { pending.get(d.id)(d); pending.delete(d.id) } })
  const call = (method, params = {}) => new Promise((r) => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })) })
  const evaluate = async (expr) => {
    const r = await call('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })
    if (r.result.exceptionDetails) throw new Error(JSON.stringify(r.result.exceptionDetails))
    return r.result.result.value
  }
  return { call, evaluate, close: () => { ws.close(); chrome.kill() } }
}

;(async () => {
  execFileSync('sh', [path.join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
  fs.mkdirSync(OUT, { recursive: true })
  const c = await cdp()
  await c.call('Page.enable')
  // Record what the page's xterm.js sends back (query replies) from the start.
  await c.call('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__sent = []
    const S = WebSocket.prototype.send
    WebSocket.prototype.send = function (d) { window.__sent.push([performance.now(), d]); return S.call(this, d) }` })
  await c.call('Page.navigate', { url: `http://127.0.0.1:${PORT}/?window=commander:1&client=web` })
  await sleep(1500)
  const screenText = `(() => { const b = __term.buffer.active; let s = []; for (let i = 0; i < __term.rows; i++) s.push(b.getLine(b.viewportY + i).translateToString(true)); return s.join('\\n') })()`
  console.log('page terminal size:', await c.evaluate('`${__term.cols}x${__term.rows}`'))
  const first = await c.call('Page.captureScreenshot', { format: 'png' })
  fs.writeFileSync(path.join(OUT, 'browser-attach.png'), Buffer.from(first.result.data, 'base64'))
  console.log('replies xterm.js sent to tmux:', JSON.stringify(await c.evaluate(`__sent.map(([t, d]) => JSON.parse(d)).filter(m => m.t === 'i').map(m => m.d)`)))

  // Keystroke latency: term.input() is what a real keypress does (fires
  // onData -> ws). Stop the clock when xterm.js has PARSED the echo.
  const lat = await c.evaluate(`(async () => {
    const out = []
    for (let i = 0; i < 100; i++) {
      const k = String.fromCharCode(97 + (i % 26))
      const t0 = performance.now()
      await new Promise((res) => {
        const want = "IN b'" + k + "'"
        const d = __term.onWriteParsed(() => {
          const b = __term.buffer.active
          for (let y = 0; y < __term.rows; y++) if (b.getLine(b.viewportY + y).translateToString(true) === want && y === b.cursorY - 1) { d.dispose(); res(); return }
        })
        __term.input(k, true)
      })
      out.push(performance.now() - t0)
      await new Promise((r) => setTimeout(r, 5))
    }
    out.sort((a, b) => a - b)
    return { n: out.length, p50: out[50], p95: out[95], max: out[99] }
  })()`)
  console.log(`browser keystroke -> echo parsed in xterm.js: n=${lat.n} p50=${lat.p50.toFixed(2)}ms p95=${lat.p95.toFixed(2)}ms max=${lat.max.toFixed(2)}ms`)

  // Lone Esc right after attach (tmux may still await query replies).
  const esc = await c.evaluate(`(async () => {
    const out = []
    for (let i = 0; i < 3; i++) {
      const t0 = performance.now()
      await new Promise((res) => {
        const d = __term.onWriteParsed(() => {
          const b = __term.buffer.active
          if (b.getLine(b.viewportY + b.cursorY - 1).translateToString(true) === "IN b'\\\\x1b'") { d.dispose(); res() }
        })
        __term.input('\\x1b', true)
      })
      out.push(Math.round(performance.now() - t0))
      __term.input('.', true)
      await new Promise((r) => setTimeout(r, 100))
    }
    return out
  })()`)
  console.log('lone Esc -> echo (ms), ~3s after attach:', esc.join(', '))

  // Same page with the query-reply shim disabled, Esc measured early and late.
  const escProbe = `(async () => {
    const t0 = performance.now()
    await new Promise((res) => {
      const d = __term.onWriteParsed(() => {
        const b = __term.buffer.active
        if (b.getLine(b.viewportY + b.cursorY - 1).translateToString(true) === "IN b'\\\\x1b'") { d.dispose(); res() }
      })
      __term.input('\\x1b', true)
    })
    const ms = Math.round(performance.now() - t0)
    __term.input('.', true)
    return ms
  })()`
  for (const variant of ['noreply=1', '']) {
    await c.call('Page.navigate', { url: `http://127.0.0.1:${PORT}/?window=commander:1&client=web2&${variant}` })
    await sleep(800)
    const early = await c.evaluate(escProbe)
    await sleep(6000)
    const late = await c.evaluate(escProbe)
    console.log(`lone Esc ${variant ? 'WITHOUT' : 'WITH'} the XTVERSION/996n reply shim: ~1s after attach ${early}ms, ~7s after attach ${late}ms`)
  }

  console.log('--- page screen ---\n' + (await c.evaluate(screenText)).replace(/\n+$/, ''))
  const shot = await c.call('Page.captureScreenshot', { format: 'png' })
  fs.writeFileSync(path.join(OUT, 'browser.png'), Buffer.from(shot.result.data, 'base64'))
  console.log('screenshot:', path.join(OUT, 'browser.png'))
  c.close()
  process.exit(0)
})().catch((e) => { console.error(e.stack || e); process.exit(1) })
