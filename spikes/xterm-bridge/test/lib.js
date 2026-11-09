// Test helpers: a scripted "browser" (ws + @xterm/headless) and tmux probes.
'use strict'
const { execFileSync } = require('child_process')
const WebSocket = require('ws')
const { Terminal } = require('@xterm/headless')
const pty = require('node-pty')

const SOCK = process.env.SOCK || '/tmp/cl.s1/sock'
const PORT = Number(process.env.PORT || 7681)
const env = { ...process.env }
delete env.TMUX

function tmux(...args) {
  return execFileSync('tmux', ['-S', SOCK, ...args], { env, encoding: 'utf8' }).trim()
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

// Browser: what xterm.js would see, minus the DOM.
class Browser {
  constructor({ window = 'commander:1', client = 'a', cols = 100, rows = 30, extra = '' } = {}) {
    this.term = new Terminal({ cols, rows, allowProposedApi: true, scrollback: 1000 })
    this.url = `ws://127.0.0.1:${PORT}/pty?window=${encodeURIComponent(window)}&client=${client}&cols=${cols}&rows=${rows}${extra}`
    this.raw = ''
  }
  open() {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.url)
      this.ws.on('message', (d) => {
        const s = d.toString()
        this.raw += s
        this.term.write(s)
        if (this.onOut) this.onOut(s)
      })
      // Like the page: the terminal's own replies (DA, OSC colour queries...)
      // go back to tmux. Without this tmux waits ~500ms on every lone Esc
      // because its terminal queries are still outstanding.
      if (!this.noReplies) {
        this.term.onData((d) => this.ws.readyState === 1 && this.send(d))
        // Same shim as public/index.html (XTVERSION + colour-scheme report).
        this.term.parser.registerCsiHandler({ prefix: '>', final: 'q' }, () => { this.send('\x1bP>|xterm.js\x1b\\'); return true })
        this.term.parser.registerCsiHandler({ prefix: '?', final: 'n' }, (p) => { if (p[0] === 996) { this.send('\x1b[?997;1n'); return true } return false })
        // Headless xterm has no theme, so it doesn't answer OSC 10/11 colour
        // queries the way the browser build does; answer like the page would.
        for (const [n, rgb] of [[10, 'cdcd/d6d6/f4f4'], [11, '1e1e/1e1e/2e2e']]) {
          this.term.parser.registerOscHandler(n, (d) => { if (d === '?') { this.send(`\x1b]${n};rgb:${rgb}\x1b\\`); return true } return false })
        }
      }
      this.ws.on('open', resolve)
      this.ws.on('error', reject)
    })
  }
  send(d) { this.ws.send(JSON.stringify({ t: 'i', d })) }
  resize(c, r) { this.term.resize(c, r); this.ws.send(JSON.stringify({ t: 'r', c, r })) }
  close() { this.ws.close() }
  screen() {
    const b = this.term.buffer.active
    const out = []
    for (let i = 0; i < this.term.rows; i++) out.push(b.getLine(b.viewportY + i)?.translateToString(true) ?? '')
    return out.join('\n')
  }
  async waitFor(re, ms = 3000) {
    const t0 = Date.now()
    while (Date.now() - t0 < ms) {
      if (re.test(this.screen())) return true
      await sleep(10)
    }
    throw new Error(`timeout waiting for ${re}\n--- screen ---\n${this.screen()}`)
  }
}

// A plain terminal client attached to the real `commander` session: stands in
// for the user's Ghostty window showing the TUI cockpit.
function realClient(cols, rows) {
  const p = pty.spawn('tmux', ['-S', SOCK, 'attach-session', '-t', 'commander'], { name: 'xterm-256color', cols, rows, env })
  p.onData(() => {})
  return p
}

function panes(win = 'commander:1') {
  return tmux('list-panes', '-t', win, '-F', '#{pane_id}=#{pane_width}x#{pane_height}').split('\n').join(' ')
}
function windowInfo(win = 'commander:1') {
  return tmux('display-message', '-p', '-t', win, 'win=#{window_width}x#{window_height} zoomed=#{window_zoomed_flag} window-size=#{window-size}')
}
function clients() {
  return tmux('list-clients', '-F', '#{client_session} #{client_width}x#{client_height}').split('\n').join(' | ')
}
function capture(pane, extra = []) {
  return tmux('capture-pane', '-p', '-t', pane, ...extra)
}

module.exports = { tmux, sleep, Browser, realClient, panes, windowInfo, clients, capture, SOCK, PORT }
