// Feature checks through the bridge. Each check prints PASS/FAIL + evidence.
// Needs: fixture/up.sh run, server.js listening.
'use strict'
const path = require('path')
const { execFileSync } = require('child_process')
const pty = require('node-pty')
const { Browser, sleep, tmux, capture } = require('./lib')

const up = (...a) => execFileSync('sh', [path.join(__dirname, '../fixture/up.sh'), ...a], { env: { ...process.env, TMUX: '' } })
const results = []
function check(name, ok, evidence) {
  results.push([name, ok])
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}\n      ${String(evidence).split('\n').join('\n      ')}`)
}
const chatPane = () => tmux('list-panes', '-t', 'commander:1', '-F', '#{pane_id} #{@cmdr_chat}').split('\n').find((l) => l.endsWith(' 1')).split(' ')[0]
const lastIn = (b, n = 1) => b.screen().split('\n').filter((l) => l.startsWith('IN ')).slice(-n).join(' ; ')

async function main() {
  up()
  const chat = chatPane()
  const chatPid = tmux('display', '-p', '-t', chat, '#{pane_pid}')

  // 1. Output renders, incl. 24-bit colour and wide unicode.
  let b = new Browser({ client: 'f1', cols: 100, rows: 30 })
  await b.open(); await b.waitFor(/TRUECOLOR-orange/)
  const line = b.term.buffer.active.getLine(b.screen().split('\n').findIndex((l) => l.includes('TRUECOLOR')) + b.term.buffer.active.viewportY)
  const cell = line.getCell(0)
  const rgb = cell.isFgRGB() ? cell.getFgColor().toString(16) : `mode=${cell.getFgColorMode()} color=${cell.getFgColor()}`
  check('24-bit colour reaches xterm.js as RGB', cell.isFgRGB() && cell.getFgColor() === 0xff6400, `fg of "T" = ${rgb} (want ff6400)`)
  const text = line.translateToString(true)
  const jaX = text.indexOf('日')
  const wide = line.getCell(jaX)?.getWidth()
  check('unicode/wide chars render', /héllo ✓ 日本語 🚀/.test(text) && wide === 2, `line="${text}" width(日)=${wide}`)
  check('xterm.js is in tmux alternate screen (no local scrollback)', b.term.buffer.active.type === 'alternate', `buffer=${b.term.buffer.active.type}`)

  // 2. Input reaches only the chat pane.
  b.send('only-chat\r'); await b.waitFor(/IN b'only-chat\\r'/)
  const others = ['%1', '%3'].map((p) => `${p}: ${capture(p).includes('only-chat') ? 'GOT IT' : 'clean'}`).join(', ')
  check('input reaches ONLY the chat pane', !others.includes('GOT'), `chat: ${lastIn(b)} | ${others}`)

  // 3. Ctrl-keys and Esc pass through.
  const keys = { 'C-c': '\x03', 'C-a (the user prefix)': '\x01', 'C-b (tmux default prefix)': '\x02', 'C-g (cmdr root bind)': '\x07', 'C-z': '\x1a', 'C-l': '\x0c', 'C-r': '\x12', 'Esc': '\x1b', 'S-Left (~/.tmux.conf root bind)': '\x1b[1;2D', 'Alt-b': '\x1bb' }
  // Ground truth is the pane itself (capture-pane), not the redraw stream.
  const paneLastIn = (p) => capture(p).split('\n').filter((l) => l.startsWith('IN ')).pop()
  const got = []
  for (const [k, seq] of Object.entries(keys)) {
    b.send('<'); await sleep(100)
    b.send(seq); await sleep(200)
    const last = paneLastIn(chat)
    got.push(`${k} -> ${last === "IN b'<'" ? 'NOTHING' : last}`)
  }
  check('Ctrl-keys / Esc / Alt reach the chat (isolate=1: prefix None, empty key-table)', got.every((g) => !g.endsWith('NOTHING')), got.join('\n'))

  // 3b. Same without isolation: the global prefix/root bindings eat keys.
  const raw = new Browser({ client: 'f1raw', cols: 100, rows: 30, extra: '&isolate=0' })
  await raw.open(); await raw.waitFor(/TRUECOLOR/)
  const got2 = []
  for (const [k, seq] of Object.entries({ 'C-g (cmdr root bind)': '\x07', 'S-Left (switches window)': '\x1b[1;2D', 'C-a (prefix)': '\x01' })) {
    tmux('select-window', '-t', 'app-f1raw:1')
    raw.send('<'); await sleep(100)
    raw.send(seq); await sleep(200)
    const last = paneLastIn(chat)
    got2.push(`${k} -> ${last === "IN b'<'" ? 'EATEN by tmux' : last} (app-f1raw on window ${tmux('display', '-p', '-t', 'app-f1raw', '#{window_index}')})`)
  }
  check('WITHOUT isolation root bindings steal keys (expected FAIL-mode demo)', got2.some((g) => g.includes('EATEN')), got2.join('\n'))
  raw.close(); await sleep(100)
  tmux('select-window', '-t', 'commander:1')

  // 4. Bracketed paste.
  check('outer xterm.js has bracketed-paste mode on (so xterm.js wraps pastes)', b.term.modes.bracketedPasteMode, `term.modes.bracketedPasteMode=${b.term.modes.bracketedPasteMode}`)
  const before = b.raw.length
  b.send('\x1b[200~line1\rline2\x1b[201~'); await sleep(300)
  const pasted = (b.raw.slice(before).match(/IN (b'[^\r]*')/g) || []).join(' ; ')
  check('paste markers reach the chat intact', pasted.includes('\\x1b[200~line1\\rline2\\x1b[201~'), pasted)

  // 5. Scrollback: tmux history + wheel -> copy-mode (mouse on in app session).
  b.send('spew\r'); await b.waitFor(/line 299/)
  const hist = tmux('display', '-p', '-t', chat, '#{history_size}')
  const xtermScroll = b.term.buffer.active.length - b.term.rows
  // SGR wheel-up at (10,10), as xterm.js sends it when tmux requested mouse.
  check('outer xterm.js mouse tracking requested by tmux', b.term.modes.mouseTrackingMode !== 'none', `mouseTrackingMode=${b.term.modes.mouseTrackingMode}`)
  for (let i = 0; i < 10; i++) b.send('\x1b[<64;10;10M')
  await sleep(300)
  const mode = tmux('display', '-p', '-t', chat, 'in_mode=#{pane_in_mode} mode=#{pane_mode} scroll=#{scroll_position}')
  const top = b.screen().split('\n')[0]
  check('wheel scrolls tmux history (copy-mode) — xterm.js keeps no scrollback of its own', mode.includes('in_mode=1') && /line \d+/.test(top), `tmux history_size=${hist}, xterm.js scrollback rows=${xtermScroll}, after 10 wheel-ups: ${mode}, top row="${top}"`)
  b.send('q'); await sleep(200) // leave copy-mode
  check('q leaves copy-mode', tmux('display', '-p', '-t', chat, '#{pane_in_mode}') === '0', tmux('display', '-p', '-t', chat, 'in_mode=#{pane_in_mode}'))

  // 6. Mouse wheel when the APP asked for the mouse: forwarded to the pane.
  b.close(); await sleep(200)
  up('--mouse')
  b = new Browser({ client: 'f2', cols: 100, rows: 30 })
  await b.open(); await b.waitFor(/TRUECOLOR/)
  const before2 = b.raw.length
  b.send('\x1b[<64;10;10M'); await sleep(300)
  check('mouse-aware app gets the wheel event itself', /IN b'\\x1b\[<64;\d+;\d+M'/.test(b.raw.slice(before2)), (b.raw.slice(before2).match(/IN (b'[^\r]*')/) || [])[0])

  // 7. Reconnect after a page reload: same client id, chat untouched.
  const chat2 = chatPane()
  const pidBefore = tmux('display', '-p', '-t', chat2, '#{pane_pid}')
  b.send('before-reload\r'); await b.waitFor(/before-reload/)
  b.close(); await sleep(300)
  const sessions = tmux('list-sessions', '-F', '#{session_name}:attached=#{session_attached}').split('\n').join(' ')
  const b2 = new Browser({ client: 'f2', cols: 100, rows: 30 })
  await b2.open(); await b2.waitFor(/before-reload/)
  b2.send('after-reload\r'); await b2.waitFor(/after-reload/)
  const pidAfter = tmux('display', '-p', '-t', chat2, '#{pane_pid}')
  check('reconnect redraws history and keeps the chat process', pidBefore === pidAfter, `sessions while detached: ${sessions}\nchat pid ${pidBefore} -> ${pidAfter}; reloaded screen has "before-reload" and accepts input: ${lastIn(b2)}`)

  // 8. Keystroke echo latency: ws send -> echoed output back over ws.
  const lat = await latency((d) => b2.send(d), (cb) => { b2.onOut = cb })
  b2.close()
  // Baseline: same fake chat in a bare node-pty, no tmux, no ws.
  const direct = pty.spawn('python3', [path.join(__dirname, '../fixture/fakechat.py')], { cols: 100, rows: 30 })
  await sleep(300)
  const latD = await latency((d) => direct.write(d), (cb) => { direct.onData(cb) })
  direct.kill()
  check('keystroke echo latency measured', true, `bridge (ws -> node-pty -> tmux client -> pane -> back): ${fmt(lat)}\nbaseline (node-pty -> fakechat, no tmux/ws): ${fmt(latD)}`)

  void chatPid
  const failed = results.filter(([n, ok]) => !ok && !n.includes('expected FAIL'))
  console.log(`\n${results.length - failed.length}/${results.length} checks as expected`)
  process.exit(failed.length ? 1 : 0)
}

async function latency(send, listen, n = 200) {
  const ts = []
  let pending = null
  let buf = ''
  listen((d) => {
    buf += d
    if (pending && buf.includes(`IN b'${pending.k}'`)) { ts.push(Number(process.hrtime.bigint() - pending.t) / 1e6); pending.done() }
  })
  for (let i = 0; i < n; i++) {
    const k = String.fromCharCode(97 + (i % 26))
    buf = ''
    await new Promise((done) => { pending = { k, t: process.hrtime.bigint(), done }; send(k) })
    pending = null
    await sleep(5)
  }
  ts.sort((a, b) => a - b)
  return { n, p50: ts[Math.floor(n * 0.5)], p95: ts[Math.floor(n * 0.95)], max: ts[n - 1] }
}
const fmt = (l) => `n=${l.n} p50=${l.p50.toFixed(2)}ms p95=${l.p95.toFixed(2)}ms max=${l.max.toFixed(2)}ms`

main().catch((e) => { console.error(e.stack || e.message); process.exit(1) })
