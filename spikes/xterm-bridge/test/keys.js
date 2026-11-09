// Which keys does tmux swallow before they reach the chat? One key at a time,
// ground truth from capture-pane. Usage: node test/keys.js [extra-query]
'use strict'
const path = require('path')
const { execFileSync } = require('child_process')
const { Browser, sleep, tmux, capture } = require('./lib')

const keys = { 'C-c': '\x03', 'C-a': '\x01', 'C-b': '\x02', 'C-g': '\x07', 'C-z': '\x1a', 'C-r': '\x12', 'Esc': '\x1b', 'S-Left': '\x1b[1;2D', 'Alt-b': '\x1bb', 'S-Enter(CSI-u)': '\x1b[13;2u' }

;(async () => {
  execFileSync('sh', [path.join(__dirname, '../fixture/up.sh')], { env: { ...process.env, TMUX: '' } })
  const chat = tmux('list-panes', '-t', 'commander:1', '-F', '#{pane_id} #{@cmdr_chat}').split('\n').find((l) => l.endsWith(' 1')).split(' ')[0]
  const b = new Browser({ client: 'k', cols: 100, rows: 30, extra: process.argv[2] || '' })
  await b.open(); await b.waitFor(/TRUECOLOR/)
  console.log(tmux('show-options', '-t', 'app-k').split('\n').filter((l) => /prefix|key-table|mouse/.test(l)).join(', '))
  for (const [k, seq] of Object.entries(keys)) {
    const n0 = capture(chat, ['-S', '-']).split('\n').filter((l) => l.startsWith('IN ')).length
    b.send(seq); await sleep(250)
    const lines = capture(chat, ['-S', '-']).split('\n').filter((l) => l.startsWith('IN '))
    const win = tmux('display', '-p', '-t', 'app-k', '#{window_index}')
    console.log(`${k.padEnd(16)} -> ${lines.slice(n0).join(' ') || 'SWALLOWED'}${win !== '1' ? `  (app session moved to window ${win}!)` : ''}`)
    if (win !== '1') tmux('select-window', '-t', 'app-k:1')
  }
  b.close(); await sleep(100); process.exit(0)
})().catch((e) => { console.error(e.message); process.exit(1) })
