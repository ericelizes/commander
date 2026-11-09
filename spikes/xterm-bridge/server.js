// Spike: bridge ONE tmux pane (the cockpit chat, tagged @cmdr_chat=1) into an
// xterm.js page over a WebSocket.
//
// Per browser client: a grouped session `app-<client>` (shares the windows of
// the target session), status off, the target window selected, the chat pane
// selected + zoomed, then `tmux attach` to it in a node-pty. Reconnecting with
// the same client id re-attaches the same grouped session; the chat process is
// never touched.
//
//   SOCK=/tmp/cl.s1/sock PORT=7681 node server.js
//   open http://127.0.0.1:7681/?window=commander:1&client=a
//
// WS protocol (text frames, JSON): client->server {t:'i',d:'<input>'} and
// {t:'r',c:cols,r:rows}; server->client: raw terminal output (text frames).
// Options via query: window=<session:window>, client=<id>, cols, rows,
// zoom=0 (don't zoom), isolate=0 (keep tmux prefix/root bindings), wsize=<window-size option for the app session's windows>.
'use strict'
const http = require('http')
const fs = require('fs')
const path = require('path')
const { execFileSync } = require('child_process')
const pty = require('node-pty')
const { WebSocketServer } = require('ws')

const SOCK = process.env.SOCK || '/tmp/cl.s1/sock'
const PORT = Number(process.env.PORT || 7681)

const tmuxEnv = { ...process.env }
delete tmuxEnv.TMUX // never talk to the user's live server by accident

function tmux(...args) {
  return execFileSync('tmux', ['-S', SOCK, ...args], { env: tmuxEnv, encoding: 'utf8' }).trim()
}

// chatPane returns the pane id tagged @cmdr_chat=1 in the window.
function chatPane(windowTarget) {
  const lines = tmux('list-panes', '-t', windowTarget, '-F', '#{pane_id} #{@cmdr_chat}').split('\n')
  const hit = lines.find((l) => l.endsWith(' 1'))
  if (!hit) throw new Error(`no @cmdr_chat pane in ${windowTarget}`)
  return hit.split(' ')[0]
}

function hasSession(name) {
  try { tmux('has-session', '-t', `=${name}`); return true } catch { return false }
}

// bindMouseTable gives the otherwise empty `cmdr-app` key table copies of the
// root table's pane mouse bindings (wheel -> copy-mode, click, drag-select),
// so the wheel still scrolls tmux history while no keyboard binding fires.
function bindMouseTable() {
  const lines = tmux('list-keys', '-T', 'root').split('\n')
    .filter((l) => /-T root +(Mouse\w*Pane|Wheel\w*Pane|\w*Click1Pane) /.test(l))
    .map((l) => l.replace('-T root', '-T cmdr-app'))
  const file = path.join(require('os').tmpdir(), `cmdr-app-keys-${process.pid}.conf`)
  fs.writeFileSync(file, lines.join('\n') + '\n')
  tmux('source-file', file)
}

// prepare creates (or reuses) the grouped session and points it at the chat.
function prepare(windowTarget, client, opts) {
  const base = windowTarget.split(':')[0]
  const app = `app-${client}`
  const pane = chatPane(windowTarget)
  const windowId = tmux('display-message', '-p', '-t', pane, '#{window_id}')
  if (!hasSession(app)) {
    tmux('new-session', '-d', '-t', base, '-s', app)
  }
  tmux('set-option', '-t', app, 'status', 'off')
  // Detaching the browser must not destroy the grouped session (reconnect).
  tmux('set-option', '-t', app, 'destroy-unattached', 'off')
  if (opts.isolate) {
    // Every key goes to the chat: no prefix (the user's is C-a = readline
    // start-of-line) and an empty default key table, so global root bindings
    // (cmdr's C-g, ~/.tmux.conf's S-arrows) don't fire in this session.
    // Mouse on: the wheel scrolls tmux history (copy-mode) or reaches the
    // pane if the app asked for the mouse.
    tmux('set-option', '-t', app, 'prefix', 'None')
    tmux('set-option', '-t', app, 'prefix2', 'None')
    tmux('set-option', '-t', app, 'key-table', 'cmdr-app')
    bindMouseTable()
    tmux('set-option', '-t', app, 'mouse', 'on')
  }
  if (opts.wsize) tmux('set-option', '-w', '-t', windowId, 'window-size', opts.wsize)
  tmux('select-window', '-t', `${app}:${windowId}`)
  tmux('select-pane', '-t', pane)
  if (opts.zoom && tmux('display-message', '-p', '-t', pane, '#{window_zoomed_flag}') !== '1') {
    tmux('resize-pane', '-Z', '-t', pane)
  }
  return { app, pane, windowId }
}

const STATIC = {
  '/': path.join(__dirname, 'public/index.html'),
  '/xterm.js': require.resolve('@xterm/xterm/lib/xterm.js'),
  '/xterm.css': require.resolve('@xterm/xterm/css/xterm.css'),
  '/addon-fit.js': require.resolve('@xterm/addon-fit/lib/addon-fit.js'),
}

const server = http.createServer((req, res) => {
  const file = STATIC[new URL(req.url, 'http://x').pathname]
  if (!file) { res.writeHead(404); return res.end() }
  const type = file.endsWith('.css') ? 'text/css' : file.endsWith('.html') ? 'text/html' : 'text/javascript'
  res.writeHead(200, { 'content-type': type })
  fs.createReadStream(file).pipe(res)
})

const wss = new WebSocketServer({ server, path: '/pty' })
wss.on('connection', (ws, req) => {
  const q = new URL(req.url, 'http://x').searchParams
  const windowTarget = q.get('window') || 'commander:1'
  const client = (q.get('client') || 'a').replace(/[^a-zA-Z0-9_-]/g, '')
  const cols = Number(q.get('cols') || 100)
  const rows = Number(q.get('rows') || 30)
  let target
  try {
    target = prepare(windowTarget, client, {
      zoom: q.get('zoom') !== '0',
      wsize: q.get('wsize'),
      isolate: q.get('isolate') !== '0',
    })
  } catch (e) {
    ws.send(`\r\nbridge error: ${e.message}\r\n`)
    return ws.close()
  }
  const term = pty.spawn('tmux', ['-S', SOCK, 'attach-session', '-t', target.app], {
    name: 'xterm-256color',
    cols, rows,
    env: { ...tmuxEnv, COLORTERM: 'truecolor' },
  })
  log(`attach ${target.app} -> ${target.pane} ${cols}x${rows} pid=${term.pid}`)
  term.onData((d) => { if (ws.readyState === 1) ws.send(d) })
  term.onExit(() => ws.close())
  ws.on('message', (raw) => {
    const m = JSON.parse(raw.toString())
    if (m.t === 'i') term.write(m.d)
    else if (m.t === 'r') term.resize(m.c, m.r)
  })
  // Killing the pty only detaches this tmux client; the grouped session and the
  // chat process stay.
  ws.on('close', () => { try { term.kill() } catch {} log(`detach ${target.app}`) })
})

function log(s) { if (process.env.QUIET !== '1') console.log(s) }

server.listen(PORT, '127.0.0.1', () => log(`xterm-bridge on http://127.0.0.1:${PORT} sock=${SOCK}`))
