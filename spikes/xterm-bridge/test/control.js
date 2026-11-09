// Control-mode probe (`tmux -C`): just enough to judge the alternative.
// Seeds a headless xterm from capture-pane, then follows %output for ONE pane
// (the @cmdr_chat pane), sends input with `send-keys -H`, and measures echo.
'use strict'
const path = require('path')
const { spawn, execFileSync } = require('child_process')
const { Terminal } = require('@xterm/headless')
const { tmux, sleep, SOCK } = require('./lib')

// %output payload: bytes < 0x20 and '\' arrive as \ooo octal; everything else raw UTF-8.
function unescapeOutput(s) {
  const out = []
  const buf = Buffer.from(s, 'utf8')
  for (let i = 0; i < buf.length; i++) {
    if (buf[i] === 0x5c && i + 3 < buf.length + 1 && /^[0-7]{3}$/.test(buf.toString('latin1', i + 1, i + 4))) {
      out.push(parseInt(buf.toString('latin1', i + 1, i + 4), 8)); i += 3
    } else out.push(buf[i])
  }
  return Buffer.from(out)
}

;(async () => {
  execFileSync('sh', [path.join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
  const chat = tmux('list-panes', '-t', 'commander:1', '-F', '#{pane_id} #{@cmdr_chat}').split('\n').find((l) => l.endsWith(' 1')).split(' ')[0]
  const [w, h] = tmux('display', '-p', '-t', chat, '#{pane_width} #{pane_height}').split(' ').map(Number)
  const winBefore = tmux('display', '-p', '-t', 'commander:1', '#{window_width}x#{window_height}')

  const env = { ...process.env }; delete env.TMUX
  const cc = spawn('tmux', ['-S', SOCK, '-C', 'attach-session', '-t', 'commander'], { env })
  const term = new Terminal({ cols: w, rows: h, allowProposedApi: true, scrollback: 5000 })
  let pending = ''
  let outputs = 0
  const listeners = []
  cc.stdout.on('data', (d) => {
    pending += d.toString('utf8')
    let nl
    while ((nl = pending.indexOf('\n')) >= 0) {
      const line = pending.slice(0, nl); pending = pending.slice(nl + 1)
      const m = line.match(/^%output (%\d+) (.*)$/)
      if (m && m[1] === chat) {
        outputs++
        const bytes = unescapeOutput(m[2])
        term.write(bytes)
        listeners.forEach((f) => f(bytes.toString('utf8')))
      }
    }
  })
  const cmd = (s) => cc.stdin.write(s + '\n')
  const sendBytes = (s) => cmd(`send-keys -t ${chat} -H ${Buffer.from(s).toString('hex').match(/../g).join(' ')}`)
  await sleep(300)

  // Seed: the screen as it is now (control mode only streams NEW output).
  const snap = tmux('capture-pane', '-p', '-e', '-t', chat).replace(/\n/g, '\r\n')
  const [cx, cy] = tmux('display', '-p', '-t', chat, '#{cursor_x} #{cursor_y}').split(' ').map(Number)
  term.write(snap + `\x1b[${cy + 1};${cx + 1}H`)
  await sleep(100)
  const screen = () => { const b = term.buffer.active; const o = []; for (let i = 0; i < term.rows; i++) o.push(b.getLine(b.viewportY + i).translateToString(true)); return o.join('\n') }
  const line0 = term.buffer.active.getLine(term.buffer.active.viewportY)
  console.log(`seeded ${w}x${h} from capture-pane -e; first cell fg RGB=${line0.getCell(0).isFgRGB()} ${line0.getCell(0).getFgColor().toString(16)} (capture-pane -e downgrades? see report)`)

  // Input + latency via send-keys -H.
  const lat = []
  for (let i = 0; i < 200; i++) {
    const k = String.fromCharCode(97 + (i % 26))
    const t0 = process.hrtime.bigint()
    await new Promise((res) => { let acc = ''; const f = (s) => { acc += s; if (acc.includes(`IN b'${k}'`)) { listeners.splice(listeners.indexOf(f), 1); res() } }; listeners.push(f); sendBytes(k) })
    lat.push(Number(process.hrtime.bigint() - t0) / 1e6)
    await sleep(5)
  }
  lat.sort((a, b) => a - b)
  console.log(`control-mode keystroke echo: n=200 p50=${lat[100].toFixed(2)}ms p95=${lat[190].toFixed(2)}ms max=${lat[199].toFixed(2)}ms`)

  // Lone Esc: no outer-terminal parsing, so no escape-time ambiguity.
  let t0 = Date.now()
  await new Promise((res) => { const f = (s) => { if (s.includes("IN b'\\x1b'")) { listeners.splice(listeners.indexOf(f), 1); res() } }; listeners.push(f); sendBytes('\x1b') })
  console.log(`lone Esc via send-keys -H: ${Date.now() - t0}ms`)

  // Native scrollback: the raw stream lands in xterm.js's own buffer.
  sendBytes('spew\r')
  await sleep(500)
  const b = term.buffer.active
  console.log(`after 300 lines: xterm.js scrollback rows=${b.length - term.rows}, buffer=${b.type}; oldest kept line="${b.getLine(0).translateToString(true)}"`)
  console.log(`window size with the control client attached (no refresh-client -C): before ${winBefore}, now ${tmux('display', '-p', '-t', 'commander:1', '#{window_width}x#{window_height}')}; %output lines for chat: ${outputs}`)
  console.log('--- headless screen (last 3 lines) ---\n' + screen().split('\n').filter((l) => l.trim()).slice(-3).join('\n'))
  cc.stdin.end(); cc.kill()
  process.exit(0)
})().catch((e) => { console.error(e.stack || e); process.exit(1) })
